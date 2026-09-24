// Command service runs the HTTP server.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sethvargo/go-envconfig"
	"go.uber.org/zap"

	"github.com/uhdaptuhbl/template-go-http-server/internal/buildinfo"
	"github.com/uhdaptuhbl/template-go-http-server/internal/config"
	"github.com/uhdaptuhbl/template-go-http-server/internal/logging"
	"github.com/uhdaptuhbl/template-go-http-server/internal/server"
	"github.com/uhdaptuhbl/template-go-http-server/internal/signals"
	"github.com/uhdaptuhbl/template-go-http-server/internal/telemetry"
	"github.com/uhdaptuhbl/template-go-http-server/internal/web"
)

// version is the build's reported version, overridden at link time with
// -ldflags "-X main.version=...". It is reported as service.version in
// telemetry.
var version = "dev"

// commit is the short git hash the binary was built from, overridden at link
// time with -ldflags "-X main.commit=...".
var commit = "unknown"

// buildDate is when the binary was built, in RFC 3339, overridden at link time
// with -ldflags "-X main.buildDate=...".
var buildDate = "unknown"

// telemetryShutdownTimeout bounds the final trace flush so an unreachable
// collector cannot hold up process exit.
const telemetryShutdownTimeout = 5 * time.Second

func main() {
	os.Exit(start())
}

// start installs the process signal handlers, runs the server, and returns the
// status the process should exit with.
//
// It is separate from main so that os.Exit runs after its deferred cleanup
// rather than skipping it.
func start() int {
	// Cancelled by the first interrupt or SIGTERM, which is what starts the
	// graceful shutdown sequence.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A channel of its own, because NotifyContext does not expose the one it
	// uses. Every channel registered for a signal receives it, so this one
	// sees the first signal too, which ForceExitOnSecond discards.
	//
	// Registered before any start-up work rather than next to the goroutine
	// that reads it, which cannot start until there is a logger: buffering
	// from this point means a signal arriving during start-up is not lost.
	// Sized for both signals, since nothing reads the channel until then.
	forced := make(chan os.Signal, 2)
	signal.Notify(forced, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(forced)

	// A nil lookuper reads the real process environment.
	if err := run(ctx, forced, nil, os.Exit); err != nil {
		fmt.Fprintf(os.Stderr, "service: %v\n", err)

		return 1
	}

	return 0
}

// run wires the process together and blocks until ctx is cancelled and the
// listeners have drained.
//
// Everything it does not create itself arrives as a parameter: the shutdown
// trigger as ctx, the second-signal channel as forced, the environment as
// lookuper, and process termination as exit. main supplies the real ones, and a
// test supplies its own to drive a whole start-up and shutdown without sending
// signals, mutating the environment, or terminating the test binary.
//
// It returns an error rather than calling os.Exit from deep in the call stack,
// which would skip deferred cleanup.
func run(ctx context.Context, forced <-chan os.Signal, lookuper envconfig.Lookuper, exit func(int)) error {
	// Taken before any start-up work, so what the version endpoint reports is
	// when the process began rather than when its last dependency finished
	// initialising.
	var started = time.Now()

	// Configuration is resolved first, because everything below is built from
	// it.
	cfg, err := config.Load(ctx, lookuper, version)
	if err != nil {
		// Returned unwrapped: Load already names both the operation and the
		// stage that failed, so another layer would only repeat itself.
		//nolint:wrapcheck // see comment above
		return err
	}

	logger, err := logging.NewFromConfig(cfg.Log)
	if err != nil {
		return fmt.Errorf("initialising logger: %w", err)
	}

	// Syncing a terminal-backed stderr returns EINVAL on Linux and macOS, so
	// the error carries no information worth acting on or reporting here.
	//nolint:errcheck // see comment above
	defer logger.Sync()

	logging.InstallSlogDefault(logger)

	var build = buildinfo.Info{Version: version, Commit: commit, Date: buildDate}

	build.Log(logger)
	config.LogEffective(logger, cfg)

	go signals.ForceExitOnSecond(forced, logger, exit)

	ui, err := web.Handler()
	if err != nil {
		return fmt.Errorf("preparing frontend handler: %w", err)
	}

	proxies, err := server.NewTrustedProxies(cfg.Proxy)
	if err != nil {
		return fmt.Errorf("configuring trusted proxies: %w", err)
	}

	// One instance shared by both listeners and by the shutdown sequencer:
	// readiness is a property of the process, not of a socket. A dependency
	// this process cannot serve without is registered here with AddCheck, for
	// example ready.AddCheck("database", db.PingContext), and takes the
	// process out of rotation while it is unreachable.
	ready := server.NewReadiness()

	tracing, err := telemetry.SetupTracing(ctx, cfg.Telemetry, logger)
	if err != nil {
		return fmt.Errorf("initialising tracing: %w", err)
	}

	// Registered before metrics is set up, not after both: a failure there would
	// otherwise leave the tracing pipeline running with spans unflushed.
	defer shutDown(ctx, logger, "traces", tracing.Shutdown)

	metrics, err := telemetry.SetupMetrics(cfg.Telemetry, logger)
	if err != nil {
		return fmt.Errorf("initialising metrics: %w", err)
	}

	defer shutDown(ctx, logger, "metrics", metrics.Shutdown)

	if recordBuildInfoErr := metrics.RecordBuildInfo(build); recordBuildInfoErr != nil {
		return fmt.Errorf("recording build information: %w", recordBuildInfoErr)
	}

	// The providers are handed to the instrumentation explicitly: nothing in
	// internal/server reads them from the otel package's globals.
	var instrumentation = server.Telemetry{
		TracerProvider: tracing.TracerProvider(),
		MeterProvider:  metrics.MeterProvider(),
		Propagator:     tracing.Propagator(),
	}

	app := server.New(cfg.Server, logger, server.NewMux(cfg.Server, proxies, ready, logger, instrumentation, ui))
	var process = server.ProcessInfo{Build: build, Started: started}

	admin := server.New(cfg.Admin, logger, server.NewAdminMux(cfg.Admin, logger, ready, process, metrics.Handler()))

	if runAllErr := server.RunAll(ctx, cfg.Lifecycle, ready, logger, app, admin); runAllErr != nil {
		return fmt.Errorf("running server: %w", runAllErr)
	}

	return nil
}

// shutDown runs one telemetry pipeline's shutdown under its own deadline,
// logging rather than returning a failure: by the time it runs the process is
// already on its way out, and nothing upstream can act on the error.
func shutDown(ctx context.Context, logger *zap.Logger, what string, stop func(context.Context) error) {
	// ctx is cancelled by the time this runs, so the deadline comes from a
	// context that does not inherit that cancellation.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryShutdownTimeout)
	defer cancel()

	if err := stop(shutdownCtx); err != nil {
		logger.Error("shutting down telemetry", zap.String("pipeline", what), zap.Error(err))
	}
}
