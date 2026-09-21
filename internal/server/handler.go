package server

import (
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Telemetry carries the OpenTelemetry providers the HTTP instrumentation
// records through. They are passed in rather than read from the otel package's
// globals, so that what a handler reports is a property of how it was built and
// a test can supply its own.
//
// The zero value records nothing: a nil field is replaced by that signal's no-op
// implementation, and a nil Propagator by one that reads and writes nothing.
type Telemetry struct {
	// TracerProvider creates the span for each request.
	TracerProvider trace.TracerProvider
	// MeterProvider records request duration and size.
	MeterProvider metric.MeterProvider
	// Propagator reads an inbound trace context from request headers.
	Propagator propagation.TextMapPropagator
}

// healthPath is the liveness endpoint, named as a constant because both the
// route and the tracing filter that excludes it have to agree.
const healthPath = "/healthz"

// healthResponse is the body returned by the health endpoint.
type healthResponse struct {
	Status string `json:"status"`
}

// NewMux returns the root HTTP handler: operational endpoints plus ui for
// everything else, wrapped in the middleware chain below. Feature routes are
// registered here as they are specified.
//
// The chain is built inside out, so the assignments below read in reverse of the
// order a request passes through them. Outermost first, a request meets:
// tracing, security headers, request identity, request logging, panic recovery,
// the in-flight limiter, the handler timeout, the body limit, and then the mux.
//
// Each placement is load bearing. Logging sits inside tracing so the span exists
// by the time an entry is written, which is what lets a log line carry the trace
// and span identifiers. Panic recovery sits inside logging so a recovered panic
// is still logged as a served request with status 500, and inside request
// identity so the panic entry carries the same request_id as everything else
// about that request. Security headers sit outside everything that writes a
// response, so the 413 from the body limit and the 503 from the limiter carry
// them too.
func NewMux(cfg Config, proxies TrustedProxies, ready *Readiness, logger *zap.Logger, telemetry Telemetry, ui http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET "+healthPath, newHealthHandler(logger))
	mux.Handle("GET "+readyPath, newReadinessHandler(ready, logger))
	mux.Handle("/", ui)

	var handler http.Handler = mux

	handler = withRequestBodyLimit(cfg.MaxRequestBodyBytes.Bytes(), logger, handler)
	handler = withHandlerTimeout(cfg.HandlerTimeout.Duration(), handler)
	handler = withInFlightLimit(cfg.MaxInFlight, logger, handler)
	handler = withPanicRecovery(logger, handler)
	handler = withRequestLogging(proxies, logger, handler)
	handler = withRequestID(proxies, handler)
	handler = withSecurityHeaders(handler)

	return newTracingHandler(telemetry, handler)
}

// newTracingHandler wraps next in OpenTelemetry HTTP instrumentation recording
// through telemetry's providers, substituting the no-op for each signal
// telemetry leaves nil. Probe requests are filtered out; shouldTrace says why.
func newTracingHandler(telemetry Telemetry, next http.Handler) http.Handler {
	tracer := telemetry.TracerProvider
	if tracer == nil {
		tracer = tracenoop.NewTracerProvider()
	}

	meter := telemetry.MeterProvider
	if meter == nil {
		meter = metricnoop.NewMeterProvider()
	}

	propagator := telemetry.Propagator
	if propagator == nil {
		propagator = propagation.NewCompositeTextMapPropagator()
	}

	return otelhttp.NewHandler(next, "http.server",
		otelhttp.WithFilter(shouldTrace),
		otelhttp.WithTracerProvider(tracer),
		otelhttp.WithMeterProvider(meter),
		otelhttp.WithPropagators(propagator),
	)
}

// shouldTrace reports whether a span should be created for r.
//
// Probes are excluded. They arrive every few seconds forever, carry no
// information about application behavior, and would otherwise dominate trace
// storage while pushing real requests out of any sampled view.
func shouldTrace(r *http.Request) bool {
	return r.URL.Path != healthPath && r.URL.Path != readyPath
}

// newHealthHandler reports that the process is up and serving. It deliberately
// checks nothing else: a readiness endpoint that probes dependencies is a
// separate concern, added when there are dependencies to probe.
func newHealthHandler(logger *zap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, logger, http.StatusOK, healthResponse{Status: "ok"})
	})
}

// withRequestLogging logs one entry per request once next has returned, and
// names the request's span after the route that matched.
//
// Both jobs have to happen after next returns, because the matched route is not
// known until the mux has routed. ServeMux records it on the same Request value
// this middleware holds, so it is readable here once ServeHTTP is done.
func withRequestLogging(proxies TrustedProxies, logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		duration := time.Since(start)
		span := trace.SpanFromContext(r.Context())

		// The default span name from HTTP instrumentation is the same for every
		// request. The matched route is the useful name: it distinguishes
		// endpoints without the unbounded cardinality of a raw path.
		if r.Pattern != "" {
			span.SetName(spanRouteName(r.Method, r.Pattern))
		}

		fields := []zap.Field{
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("route", r.Pattern),
			zap.Int("status", recorder.status),
			zap.Duration("duration", duration),
			zap.String("request_id", RequestIDFromContext(r.Context())),
			zap.String("client_ip", proxies.ClientIP(r)),
		}

		// Invalid when tracing is disabled, or on a request the tracing filter
		// excluded. Emitting the zero identifiers would imply a trace that does
		// not exist.
		if sc := span.SpanContext(); sc.IsValid() {
			fields = append(fields,
				zap.String("trace_id", sc.TraceID().String()),
				zap.String("span_id", sc.SpanID().String()),
			)
		}

		logger.Log(levelForStatus(recorder.status), "request served", fields...)
	})
}

// levelForStatus maps a response status to the level its entry is logged at.
//
// One level for every outcome makes a failure invisible in a stream dominated by
// successes: an operator filtering for errors should find the 500s without
// knowing this project's log message strings.
func levelForStatus(status int) zapcore.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return zapcore.ErrorLevel
	case status >= http.StatusBadRequest:
		return zapcore.WarnLevel
	default:
		return zapcore.InfoLevel
	}
}

// spanRouteName builds the "{method} {route}" span name the OpenTelemetry HTTP
// conventions ask for.
//
// A ServeMux pattern registered with a method already contains it, as in
// "GET /healthz", while a pattern registered without one, as in "/", does not.
// Prepending unconditionally would yield "GET GET /healthz".
func spanRouteName(method, pattern string) string {
	if strings.HasPrefix(pattern, method+" ") {
		return pattern
	}

	return method + " " + pattern
}
