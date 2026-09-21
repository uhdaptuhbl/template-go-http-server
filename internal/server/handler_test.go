package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// testMux builds the root handler with a stand-in UI handler, so route tests do
// not depend on the embedded frontend.
func testMux(t *testing.T) http.Handler {
	t.Helper()

	return NewMux(DefaultConfig(), TrustedProxies{}, NewReadiness(), zap.NewNop(), Telemetry{}, testUI(t))
}

// testUI is a stand-in for the embedded frontend handler.
func testUI(t *testing.T) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// testRoutes is the route table alone, without the tracing and logging wrappers
// NewMux applies. Middleware tests wrap it themselves so that the middleware
// under test is the only one in the chain.
func testRoutes(t *testing.T) http.Handler {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle("GET "+healthPath, newHealthHandler(zap.NewNop()))
	mux.Handle("/", testUI(t))

	return mux
}

// TestNewMuxRouting cover AC-001.4, AC-001.6, AC-001.7, and AC-002.13.
func TestNewMuxRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
	}{
		{name: "health responds ok", method: http.MethodGet, target: "/healthz", wantStatus: http.StatusOK},
		{name: "unmatched path falls through to ui", method: http.MethodGet, target: "/anything", wantStatus: http.StatusOK},
		// The "/" catch-all matches any method, so a method mismatch on a
		// specific route reaches the UI handler instead of yielding 405.
		// ServeMux only synthesises 405 when nothing else matches the request.
		{name: "wrong method on health falls through to ui", method: http.MethodPost, target: "/healthz", wantStatus: http.StatusOK},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			testMux(t).ServeHTTP(recorder, httptest.NewRequest(test.method, test.target, http.NoBody))

			if recorder.Code != test.wantStatus {
				t.Errorf("%s %s returned status %d, want %d", test.method, test.target, recorder.Code, test.wantStatus)
			}
		})
	}
}

// TestHealthResponseBody covers AC-001.6.
func TestHealthResponseBody(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	testMux(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody))

	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("got Content-Type %q, want %q", got, "application/json")
	}

	var body healthResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decoding health response: %v", err)
	}

	if body.Status != "ok" {
		t.Errorf("got status %q, want %q", body.Status, "ok")
	}
}

// TestStatusRecorderExposesTheUnderlyingWriter cites no criterion: Unwrap is
// internal plumbing between this middleware and net/http, observable only as
// the absence of the failures described at the assertion.
func TestStatusRecorderExposesTheUnderlyingWriter(t *testing.T) {
	t.Parallel()

	// http.ResponseController walks a wrapper chain by calling Unwrap. Without
	// it, any handler behind this middleware loses the ability to flush, hijack,
	// or set a deadline, which is how net/http/pprof keeps a long profile from
	// tripping the server's write timeout.
	var flushed bool

	handler := withRequestLogging(TrustedProxies{}, zap.NewNop(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush through the middleware failed: %v", err)

			return
		}

		flushed = true
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	if !flushed {
		t.Error("handler behind the middleware could not reach the underlying writer")
	}
}

// TestWithRequestLoggingPreservesTheResponse covers AC-001.14.
func TestWithRequestLoggingPreservesTheResponse(t *testing.T) {
	t.Parallel()

	handler := withRequestLogging(TrustedProxies{}, zap.NewNop(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	if recorder.Code != http.StatusTeapot {
		t.Errorf("got status %d, want %d: middleware must not alter the response", recorder.Code, http.StatusTeapot)
	}
}

// TestWithRequestLoggingEmitsOneEntryDescribingTheRequest covers AC-001.14.
func TestWithRequestLoggingEmitsOneEntryDescribingTheRequest(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zapcore.InfoLevel)
	handler := withRequestLogging(TrustedProxies{}, zap.New(core), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/some/path", http.NoBody))

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want exactly 1 per request", len(entries))
	}

	fields := entries[0].ContextMap()

	if fields["method"] != http.MethodPost {
		t.Errorf("got method field %v, want %q", fields["method"], http.MethodPost)
	}

	if fields["path"] != "/some/path" {
		t.Errorf("got path field %v, want %q", fields["path"], "/some/path")
	}

	if fields["status"] != int64(http.StatusTeapot) {
		t.Errorf("got status field %v, want %d", fields["status"], http.StatusTeapot)
	}

	if _, ok := fields["duration"]; !ok {
		t.Error("log entry has no duration field")
	}
}

// TestWithRequestLoggingReportsOKWhenTheHandlerOmitsWriteHeader covers AC-001.14.
func TestWithRequestLoggingReportsOKWhenTheHandlerOmitsWriteHeader(t *testing.T) {
	t.Parallel()

	// net/http implies 200 for a handler that writes a body without setting a
	// status. statusRecorder must report that, not the zero value it would
	// otherwise hold.
	core, logs := observer.New(zapcore.InfoLevel)
	handler := withRequestLogging(TrustedProxies{}, zap.New(core), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "body with no explicit status"); err != nil {
			t.Errorf("writing response body: %v", err)
		}
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want exactly 1 per request", len(entries))
	}

	if got := entries[0].ContextMap()["status"]; got != int64(http.StatusOK) {
		t.Errorf("got status field %v, want %d", got, http.StatusOK)
	}
}

// TestRequestLogLevelFollowsStatus covers AC-002.12.
func TestRequestLogLevelFollowsStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		wantLevel zapcore.Level
	}{
		{name: "200 logs at info", status: http.StatusOK, wantLevel: zapcore.InfoLevel},
		{name: "204 logs at info", status: http.StatusNoContent, wantLevel: zapcore.InfoLevel},
		{name: "404 logs at warn", status: http.StatusNotFound, wantLevel: zapcore.WarnLevel},
		{name: "418 logs at warn", status: http.StatusTeapot, wantLevel: zapcore.WarnLevel},
		{name: "500 logs at error", status: http.StatusInternalServerError, wantLevel: zapcore.ErrorLevel},
		{name: "503 logs at error", status: http.StatusServiceUnavailable, wantLevel: zapcore.ErrorLevel},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			core, logs := observer.New(zapcore.DebugLevel)
			handler := withRequestLogging(TrustedProxies{}, zap.New(core), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))

			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))

			entries := logs.All()
			if len(entries) != 1 {
				t.Fatalf("got %d log entries, want exactly 1 per request", len(entries))
			}

			if entries[0].Level != test.wantLevel {
				t.Errorf("got level %v, want %v", entries[0].Level, test.wantLevel)
			}
		})
	}
}

// TestRequestLogCarriesCorrelationFields covers AC-002.9 and AC-002.11.
func TestRequestLogCarriesCorrelationFields(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	handler := withRequestID(TrustedProxies{}, withRequestLogging(TrustedProxies{}, logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	request := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	request.RemoteAddr = "203.0.113.7:54321"

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want exactly 1 per request", len(entries))
	}

	fields := entries[0].ContextMap()

	requestID, _ := fields["request_id"].(string)
	if requestID == "" {
		t.Error("got empty request_id field, want a non-empty identifier")
	}

	if got := recorder.Header().Get(RequestIDHeader); got != requestID {
		t.Errorf("got %s header %q, want it to match logged request_id %q", RequestIDHeader, got, requestID)
	}

	if fields["client_ip"] != "203.0.113.7" {
		t.Errorf("got client_ip field %v, want %q", fields["client_ip"], "203.0.113.7")
	}
}

// TestMuxServesReadiness covers AC-002.13.
func TestMuxServesReadiness(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	testMux(t).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))

	if recorder.Code != http.StatusOK {
		t.Errorf("got status %d, want %d", recorder.Code, http.StatusOK)
	}

	if got := strings.TrimSuffix(recorder.Body.String(), "\n"); got != `{"status":"ready"}` {
		t.Errorf("got body %q, want %q", got, `{"status":"ready"}`)
	}
}

// TestMuxRecoversHandlerPanic covers AC-002.1.
func TestMuxRecoversHandlerPanic(t *testing.T) {
	t.Parallel()

	panicUI := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})

	mux := NewMux(DefaultConfig(), TrustedProxies{}, NewReadiness(), zap.NewNop(), Telemetry{}, panicUI)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("got status %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	want := `{"errors":[{"status":"500","code":"internal","title":"Internal server error"}]}`
	if got := strings.TrimSuffix(recorder.Body.String(), "\n"); got != want {
		t.Errorf("got body %q, want %q", got, want)
	}
}

// TestMuxRecordsThroughTheSuppliedTelemetry covers AC-005.17.
func TestMuxRecordsThroughTheSuppliedTelemetry(t *testing.T) {
	t.Parallel()

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"

	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutting down tracer provider: %v", err)
		}
	})

	mux := NewMux(DefaultConfig(), TrustedProxies{}, NewReadiness(), zap.NewNop(), Telemetry{
		TracerProvider: provider,
		Propagator:     propagation.TraceContext{},
	}, testUI(t))

	request := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	request.Header.Set("Traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")

	mux.ServeHTTP(httptest.NewRecorder(), request)

	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(ended))
	}

	// The span exists only if NewMux passed the provider to the
	// instrumentation, and its trace ID matches the inbound one only if the
	// supplied propagator read the header. Neither is reachable through a
	// global here, because nothing in this test installs one.
	if got := ended[0].SpanContext().TraceID().String(); got != traceID {
		t.Errorf("span has trace ID %q, want the inbound %q", got, traceID)
	}
}

// TestMuxWithZeroTelemetryIgnoresInboundTraceHeaders covers AC-005.17.
func TestMuxWithZeroTelemetryIgnoresInboundTraceHeaders(t *testing.T) {
	t.Parallel()

	var captured trace.SpanContext

	ui := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = trace.SpanContextFromContext(r.Context())
	})

	mux := NewMux(DefaultConfig(), TrustedProxies{}, NewReadiness(), zap.NewNop(), Telemetry{}, ui)

	request := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	request.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	mux.ServeHTTP(httptest.NewRecorder(), request)

	// A zero value records nothing and reads nothing: the inbound header is not
	// extracted, so whatever a process installed globally cannot change what
	// this handler sees.
	if captured.IsValid() {
		t.Errorf("zero-value Telemetry extracted a span context from an inbound traceparent: %v", captured)
	}
}

// TestNewMuxDoesNotServeAdministrativeRoutes covers AC-001.9 and AC-001.10.
func TestNewMuxDoesNotServeAdministrativeRoutes(t *testing.T) {
	t.Parallel()

	// net/http/pprof registers its handlers on http.DefaultServeMux from an
	// init function, and this package imports it for the administrative mux.
	// A root mux built from the default one would therefore serve profiling
	// on the public listener without a line of code saying so. The sentinel
	// proves the frontend answered instead: these paths are not routes here,
	// so they fall through the "/" catch-all like any other unmatched path.
	sentinel := "frontend fallthrough"
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)

		if _, err := io.WriteString(w, sentinel); err != nil {
			t.Errorf("writing sentinel body: %v", err)
		}
	})

	mux := NewMux(DefaultConfig(), TrustedProxies{}, NewReadiness(), zap.NewNop(), Telemetry{}, ui)

	administrative := []string{
		"/metrics",
		"/debug/pprof/",
		"/debug/pprof/cmdline",
		"/debug/pprof/symbol",
		"/debug/pprof/trace",
	}

	for _, path := range administrative {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, http.NoBody))

			if got := recorder.Body.String(); got != sentinel {
				t.Errorf("GET %s on the application listener returned %q, want the frontend handler's %q",
					path, got, sentinel)
			}
		})
	}
}
