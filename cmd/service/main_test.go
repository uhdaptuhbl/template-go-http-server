package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/sethvargo/go-envconfig"
)

// Tests here are deliberately not parallel: run installs the slog default and
// the OpenTelemetry SDK error handler, both of which are process-wide.

// freeAddr returns a loopback address with a port the kernel has just handed
// out and nothing is listening on.
//
// Port 0 would be the direct way to ask for an ephemeral port, but
// configuration refuses one: nothing can be told to route to a port that is
// only known once the bind has happened. The address is therefore resolved
// before configuration is loaded rather than by it.
func freeAddr(t *testing.T) string {
	t.Helper()

	var config net.ListenConfig

	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}

	addr := listener.Addr().String()

	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("releasing the reserved port: %v", closeErr)
	}

	return addr
}

// testEnv is an environment for a process that binds free loopback ports and
// drains without delay, so a test can start and stop the real wiring in the
// time an ordinary unit test is allowed.
func testEnv(t *testing.T) map[string]string {
	t.Helper()

	return map[string]string{
		"SERVICE_SERVER_ADDR":               freeAddr(t),
		"SERVICE_ADMIN_ADDR":                freeAddr(t),
		"SERVICE_LIFECYCLE_PRE_DRAIN_DELAY": "0s",
		"SERVICE_LOG_LEVEL":                 "error",
	}
}

// TestRunStartsAndShutsDownCleanly covers AC-005.19.
func TestRunStartsAndShutsDownCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	// Cancelled before run is called: the listeners still bind, and the
	// shutdown sequence then runs immediately. What is under test is that the
	// whole process wiring builds and tears down without error, not how long it
	// stays up, so there is nothing here to wait for and nothing to race.
	cancel()

	// Never written to, so the force-exit goroutine stays parked and exit is
	// never reached on this path.
	forced := make(chan os.Signal)

	err := run(ctx, forced, envconfig.MapLookuper(testEnv(t)), func(code int) {
		t.Errorf("exit(%d) was called on the ordinary shutdown path", code)
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
}

// TestRunReportsAConfigurationFailure covers AC-005.19.
func TestRunReportsAConfigurationFailure(t *testing.T) {
	env := testEnv(t)
	env["SERVICE_SERVER_ADDR"] = "not-a-host-port"

	// Returns before anything blocks, so an uncancelled context is safe here
	// and a regression that started the servers anyway would fail the test by
	// hanging rather than by passing quietly.
	err := run(t.Context(), make(chan os.Signal), envconfig.MapLookuper(env), func(code int) {
		t.Errorf("exit(%d) was called for a configuration failure", code)
	})
	if err == nil {
		t.Fatal("run returned nil for an invalid server address")
	}

	if !strings.Contains(err.Error(), "configuration") {
		t.Errorf("run returned %q, which does not name configuration as the failing stage", err)
	}
}
