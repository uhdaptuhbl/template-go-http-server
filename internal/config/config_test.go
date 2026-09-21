package config

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/sethvargo/go-envconfig"
	"go.uber.org/zap/zapcore"

	"github.com/uhdaptuhbl/template-go-http-server/internal/configtype"
	"github.com/uhdaptuhbl/template-go-http-server/internal/logging"
	"github.com/uhdaptuhbl/template-go-http-server/internal/server"
	"github.com/uhdaptuhbl/template-go-http-server/internal/telemetry"
)

// TestDefaultComposesEachPackagesOwnDefaults covers AC-001.20.
func TestDefaultComposesEachPackagesOwnDefaults(t *testing.T) {
	t.Parallel()

	got := Default()

	want := Config{
		Log:       logging.DefaultConfig(),
		Server:    server.DefaultConfig(),
		Admin:     server.DefaultAdminConfig(),
		Lifecycle: server.DefaultLifecycleConfig(),
		Proxy:     server.DefaultProxyConfig(),
		Telemetry: telemetry.DefaultConfig(),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Default() mismatch (-want +got):\n%s", diff)
	}
}

// TestDefaultBindsTheTwoListenersToDifferentPorts covers AC-001.5.
func TestDefaultBindsTheTwoListenersToDifferentPorts(t *testing.T) {
	t.Parallel()

	got := Default()

	// Ports, not whole addresses. The criterion is that the two listeners take
	// separate ports; comparing the address strings would call
	// ":8080" and "127.0.0.1:8080" separate, which is the collision the
	// criterion exists to forbid.
	_, serverPort, err := net.SplitHostPort(got.Server.Addr)
	if err != nil {
		t.Fatalf("splitting Server.Addr %q: %v", got.Server.Addr, err)
	}

	_, adminPort, err := net.SplitHostPort(got.Admin.Addr)
	if err != nil {
		t.Fatalf("splitting Admin.Addr %q: %v", got.Admin.Addr, err)
	}

	// The admin listener carries profiling and metrics, so it must not land on
	// the port the application exposes to users.
	if serverPort == adminPort {
		t.Errorf("both listeners bound to port %q (server %q, admin %q), want separate ports",
			serverPort, got.Server.Addr, got.Admin.Addr)
	}
}

// TestLoadReturnsTheDefaultsWhenNothingIsSet cover AC-001.4 and AC-002.17.
func TestLoadReturnsTheDefaultsWhenNothingIsSet(t *testing.T) {
	t.Parallel()

	got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{}), "1.2.3")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	want := Default()
	want.Telemetry.ServiceVersion = "1.2.3"

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Load() mismatch (-want +got):\n%s", diff)
	}
}

// TestLoadAppliesEnvironmentValuesOverTheDefaults covers AC-001.1.
func TestLoadAppliesEnvironmentValuesOverTheDefaults(t *testing.T) {
	t.Parallel()

	got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
		"SERVICE_LOG_LEVEL":           "debug",
		"SERVICE_LOG_FORMAT":          "json",
		"SERVICE_SERVER_ADDR":         "127.0.0.1:3000",
		"SERVICE_SERVER_READ_TIMEOUT": "45s",
		"SERVICE_ADMIN_ADDR":          "127.0.0.1:3001",
		"OTEL_SERVICE_NAME":           "renamed",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
	}), "1.2.3")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	// Every field below starts non-zero in Default(). go-envconfig skips a
	// field that already holds a value unless its tag says overwrite, so a
	// missing overwrite would silently ignore the whole environment here.
	want := Default()
	want.Log.Level = zapcore.DebugLevel
	want.Log.Format = logging.FormatJSON
	want.Server.Addr = "127.0.0.1:3000"
	want.Server.ReadTimeout = configtype.Duration(45 * time.Second)
	want.Admin.Addr = "127.0.0.1:3001"
	want.Telemetry.ServiceName = "renamed"
	want.Telemetry.ServiceVersion = "1.2.3"
	want.Telemetry.OTLPEndpoint = "http://collector:4318"

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Load() mismatch (-want +got):\n%s", diff)
	}
}

// TestLoadConfiguresTheTwoListenersIndependently covers AC-001.5.
func TestLoadConfiguresTheTwoListenersIndependently(t *testing.T) {
	t.Parallel()

	got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
		"SERVICE_SERVER_ADDR":             "127.0.0.1:3000",
		"SERVICE_SERVER_SHUTDOWN_TIMEOUT": "1s",
	}), "1.2.3")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	// Both listeners share one Config type. Setting one must not move the
	// other, or an operator retuning the public listener would silently
	// retune the administrative one with it.
	if diff := cmp.Diff(server.DefaultAdminConfig(), got.Admin); diff != "" {
		t.Errorf("admin config changed with the server's (-want +got):\n%s", diff)
	}
}

// TestLoadReadsTelemetryVariablesWithoutTheProjectPrefix covers AC-001.21.
func TestLoadReadsTelemetryVariablesWithoutTheProjectPrefix(t *testing.T) {
	t.Parallel()

	// The OpenTelemetry specification fixes these names, so every collector
	// and deployment tool already sets them. Namespacing them would mean the
	// standard variable silently did nothing.
	got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
		"SERVICE_OTEL_SERVICE_NAME": "ignored",
	}), "1.2.3")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if got.Telemetry.ServiceName != telemetry.DefaultServiceName {
		t.Errorf("ServiceName = %q, want the prefixed variable to have no effect", got.Telemetry.ServiceName)
	}
}

// TestLoadPrefersTheSignalSpecificTracesEndpoint covers AC-001.22.
func TestLoadPrefersTheSignalSpecificTracesEndpoint(t *testing.T) {
	t.Parallel()

	got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":        "http://general:4318",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://traces:4318",
	}), "1.2.3")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if endpoint := got.Telemetry.TracesEndpoint(); endpoint != "http://traces:4318" {
		t.Errorf("TracesEndpoint() = %q, want the signal-specific endpoint to win", endpoint)
	}
}

// TestLoadTakesTheServiceVersionFromTheBuildRatherThanTheEnvironment covers AC-001.23.
func TestLoadTakesTheServiceVersionFromTheBuildRatherThanTheEnvironment(t *testing.T) {
	t.Parallel()

	got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
		"OTEL_SERVICE_VERSION":              "from-env",
		"SERVICE_TELEMETRY_SERVICE_VERSION": "also-from-env",
	}), "1.2.3")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	// The reported version must match the binary that is running. Letting the
	// environment set it would let a stale deployment misreport itself.
	if got.Telemetry.ServiceVersion != "1.2.3" {
		t.Errorf("ServiceVersion = %q, want the value passed by the caller", got.Telemetry.ServiceVersion)
	}
}

// TestLoadTreatsAnEmptyVariableAsUnset covers AC-001.3.
func TestLoadTreatsAnEmptyVariableAsUnset(t *testing.T) {
	t.Parallel()

	got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
		"SERVICE_ADMIN_ADDR": "",
	}), "1.2.3")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	// Measured go-envconfig behaviour: a variable that is present but empty is
	// treated as absent, so overwrite does not fire and the default stands.
	// That is the outcome to want here. An exported-but-empty variable, which
	// a shell produces easily, would otherwise blank the listen address and
	// fail validation at startup.
	if got.Admin.Addr != server.DefaultAdminConfig().Addr {
		t.Errorf("Admin.Addr = %q, want the default to stand", got.Admin.Addr)
	}
}

// TestLoadDefaultsLifecycleAndProxy covers AC-002.15 and AC-002.17.
// It also covers AC-001.4.
func TestLoadDefaultsLifecycleAndProxy(t *testing.T) {
	t.Parallel()

	got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{}), "1.2.3")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if got.Lifecycle.PreDrainDelay != configtype.Duration(5*time.Second) {
		t.Errorf("Lifecycle.PreDrainDelay = %v, want 5s", got.Lifecycle.PreDrainDelay)
	}

	if len(got.Proxy.TrustedCIDRs) != 0 {
		t.Errorf("Proxy.TrustedCIDRs = %v, want empty", got.Proxy.TrustedCIDRs)
	}

	if got.Server.Addr != ":8080" {
		t.Errorf("Server.Addr = %q, want %q", got.Server.Addr, ":8080")
	}

	if got.Admin.Addr != server.DefaultAdminConfig().Addr {
		t.Errorf("Admin.Addr = %q, want %q", got.Admin.Addr, server.DefaultAdminConfig().Addr)
	}
}

// TestLoadOverridesLifecycleAndProxy covers AC-002.15.
func TestLoadOverridesLifecycleAndProxy(t *testing.T) {
	t.Parallel()

	got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
		"SERVICE_LIFECYCLE_PRE_DRAIN_DELAY": "0s",
		"SERVICE_PROXY_TRUSTED_CIDRS":       "10.0.0.0/8,192.168.0.0/16",
	}), "1.2.3")
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if got.Lifecycle.PreDrainDelay != 0 {
		t.Errorf("Lifecycle.PreDrainDelay = %v, want 0s", got.Lifecycle.PreDrainDelay)
	}

	want := []string{"10.0.0.0/8", "192.168.0.0/16"}
	if diff := cmp.Diff(want, got.Proxy.TrustedCIDRs); diff != "" {
		t.Errorf("Proxy.TrustedCIDRs mismatch (-want +got):\n%s", diff)
	}
}

// TestLoadRejectsInvalidTrustedCIDR covers AC-002.10 and AC-002.11.
func TestLoadRejectsInvalidTrustedCIDR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cidrs string
	}{
		{name: "not a CIDR at all", cidrs: "not-a-cidr"},
		{name: "an address with no prefix length", cidrs: "10.0.0.1"},
		{name: "one bad entry among good ones", cidrs: "10.0.0.0/8,192.168.0.0/nope"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
				"SERVICE_PROXY_TRUSTED_CIDRS": tt.cidrs,
			}), "1.2.3")
			if err == nil {
				t.Fatalf("Load() error = nil, want a failure; got %+v", got)
			}

			// A typo in a trust boundary has to name the variable that carries
			// it: the value is a list, and the message is what tells an
			// operator which list to go and look at.
			if !strings.Contains(err.Error(), "SERVICE_PROXY_") {
				t.Errorf("Load() error = %q, want it to name the proxy settings", err)
			}
		})
	}
}

// TestLoadAcceptsTheTrustedCIDRFormsTheParserAccepts covers AC-002.10.
//
// Startup validation and the parser that runs on the request path are the same
// code, so a list that starts the process is a list that parses. The forms a
// separated list picks up by being edited, a trailing comma or a doubled one,
// are empty entries rather than malformed networks, and refusing them at
// startup would fail a deployment over a value that changes nothing.
func TestLoadAcceptsTheTrustedCIDRFormsTheParserAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cidrs string
		want  []string
	}{
		{
			name:  "trailing comma",
			cidrs: "10.0.0.0/8,",
			want:  []string{"10.0.0.0/8", ""},
		},
		{
			name:  "doubled comma",
			cidrs: "10.0.0.0/8,,192.168.0.0/16",
			want:  []string{"10.0.0.0/8", "", "192.168.0.0/16"},
		},
		{
			name:  "spaces after the separators",
			cidrs: "10.0.0.0/8, 192.168.0.0/16",
			want:  []string{"10.0.0.0/8", "192.168.0.0/16"},
		},
		{
			name:  "an IPv6 network",
			cidrs: "2001:db8::/32",
			want:  []string{"2001:db8::/32"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
				"SERVICE_PROXY_TRUSTED_CIDRS": tt.cidrs,
			}), "1.2.3")
			if err != nil {
				t.Fatalf("Load() error = %v, want the list accepted", err)
			}

			// go-envconfig keeps the empty entries and trims the spaces, so
			// what reaches the parser is asserted rather than assumed.
			if diff := cmp.Diff(tt.want, got.Proxy.TrustedCIDRs); diff != "" {
				t.Errorf("Proxy.TrustedCIDRs mismatch (-want +got):\n%s", diff)
			}

			if _, parseErr := server.NewTrustedProxies(got.Proxy); parseErr != nil {
				t.Errorf("NewTrustedProxies() error = %v, want what startup accepted to parse", parseErr)
			}
		})
	}
}

// TestLoadRejectsInvalidConfiguration covers AC-001.2.
func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "listen address without a port",
			env:  map[string]string{"SERVICE_SERVER_ADDR": "localhost"},
		},
		{
			name: "timeout of zero",
			env:  map[string]string{"SERVICE_SERVER_READ_TIMEOUT": "0s"},
		},
		{
			name: "negative timeout",
			env:  map[string]string{"SERVICE_SERVER_IDLE_TIMEOUT": "-1s"},
		},
		{
			name: "collector endpoint that is not a URL",
			env:  map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "not a url"},
		},
		{
			name: "collector endpoint that is a bare host and port",
			env:  map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "localhost:4317"},
		},
		{
			name: "traces endpoint that is a bare host and port",
			env:  map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "collector:4317"},
		},
		{
			name: "unknown log format",
			env:  map[string]string{"SERVICE_LOG_FORMAT": "xml"},
		},
		{
			name: "unparseable log level",
			env:  map[string]string{"SERVICE_LOG_LEVEL": "chatty"},
		},
		{
			name: "unparseable timeout",
			env:  map[string]string{"SERVICE_SERVER_WRITE_TIMEOUT": "soon"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(t.Context(), envconfig.MapLookuper(tt.env), "1.2.3")
			if err == nil {
				t.Fatalf("Load() error = nil, want a failure; got %+v", got)
			}

			if !strings.Contains(err.Error(), "loading configuration") {
				t.Errorf("Load() error = %q, want it to name the operation", err)
			}

			// A partly-built config is worse than none: the caller might use
			// it, so failure returns the zero value.
			if diff := cmp.Diff(Config{}, got); diff != "" {
				t.Errorf("Load() returned a config alongside its error (-want +got):\n%s", diff)
			}
		})
	}
}

// TestLoadAcceptsIPv6ListenAddresses covers AC-001.1.
//
// A dual-stack deployment binds an IPv6 literal, which has to be bracketed to
// separate it from the port. Validation that only understands the hostname form
// refuses the address the operator was told to use.
func TestLoadAcceptsIPv6ListenAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
	}{
		{name: "wildcard", addr: "[::]:8080"},
		{name: "loopback", addr: "[::1]:9090"},
		{name: "ipv4 literal", addr: "0.0.0.0:8080"},
		{name: "every interface", addr: ":8080"},
		{name: "hostname", addr: "localhost:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
				"SERVICE_SERVER_ADDR": tt.addr,
			}), "1.2.3")
			if err != nil {
				t.Fatalf("Load() error = %v, want the address accepted", err)
			}

			if got.Server.Addr != tt.addr {
				t.Errorf("Server.Addr = %q, want %q", got.Server.Addr, tt.addr)
			}
		})
	}
}

// TestLoadRejectsAnEphemeralListenPort covers AC-001.2.
//
// Port 0 asks the kernel to choose, which is useful to a test holding the
// listener and useless to a deployment: nothing can route to a port that is
// only discoverable by reading a log line.
func TestLoadRejectsAnEphemeralListenPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{
			name: "application listener",
			env:  map[string]string{"SERVICE_SERVER_ADDR": "0.0.0.0:0"},
			want: []string{"SERVICE_SERVER_ADDR"},
		},
		{
			name: "administrative listener",
			env:  map[string]string{"SERVICE_ADMIN_ADDR": "[::1]:0"},
			want: []string{"SERVICE_ADMIN_ADDR"},
		},
		{
			name: "both listeners are reported together",
			env: map[string]string{
				"SERVICE_SERVER_ADDR": "127.0.0.1:0",
				"SERVICE_ADMIN_ADDR":  "127.0.0.1:0",
			},
			want: []string{"SERVICE_SERVER_ADDR", "SERVICE_ADMIN_ADDR"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(t.Context(), envconfig.MapLookuper(tt.env), "1.2.3")
			if err == nil {
				t.Fatalf("Load() error = nil, want a failure; got %+v", got)
			}

			for _, name := range tt.want {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("Load() error = %q, want it to name %s", err, name)
				}
			}
		})
	}
}

// TestLoadRequiresTheReadTimeoutToCoverTheHeaderTimeout covers AC-001.2.
//
// ReadTimeout bounds the whole request and ReadHeaderTimeout bounds its
// headers, so a ReadTimeout below it retires the header timeout without
// saying so: the total deadline always fires first, and the setting that
// exists to stop a slow-header client stops nothing.
func TestLoadRequiresTheReadTimeoutToCoverTheHeaderTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		env        map[string]string
		wantFailed bool
	}{
		{
			name: "read timeout below the header timeout",
			env: map[string]string{
				"SERVICE_SERVER_READ_HEADER_TIMEOUT": "5s",
				"SERVICE_SERVER_READ_TIMEOUT":        "2s",
			},
			wantFailed: true,
		},
		{
			name: "read timeout equal to the header timeout",
			env: map[string]string{
				"SERVICE_SERVER_READ_HEADER_TIMEOUT": "5s",
				"SERVICE_SERVER_READ_TIMEOUT":        "5s",
			},
			wantFailed: false,
		},
		{
			name: "read timeout above the header timeout",
			env: map[string]string{
				"SERVICE_SERVER_READ_HEADER_TIMEOUT": "5s",
				"SERVICE_SERVER_READ_TIMEOUT":        "30s",
			},
			wantFailed: false,
		},
		{
			name:       "the administrative listener is held to the same rule",
			env:        map[string]string{"SERVICE_ADMIN_READ_TIMEOUT": "1s"},
			wantFailed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(t.Context(), envconfig.MapLookuper(tt.env), "1.2.3")

			if tt.wantFailed {
				if err == nil {
					t.Fatalf("Load() error = nil, want a failure; got %+v", got)
				}

				if !strings.Contains(err.Error(), "ReadTimeout") {
					t.Errorf("Load() error = %q, want it to name the failing field", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("Load() error = %v, want the timeouts accepted", err)
			}
		})
	}
}

// TestLoadRequiresTheHandlerTimeoutToFitInsideTheWriteTimeout covers AC-001.2.
//
// Both deadlines start when the handler does, and the handler timeout works by
// cancelling a context the handler must then act on. A handler timeout at or
// above the write timeout therefore cannot produce an outcome anyone sees: the
// connection's write deadline has already expired by the time the cancellation
// is observed.
func TestLoadRequiresTheHandlerTimeoutToFitInsideTheWriteTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		env        map[string]string
		wantFailed bool
	}{
		{
			name:       "disabled",
			env:        map[string]string{"SERVICE_SERVER_HANDLER_TIMEOUT": "0s"},
			wantFailed: false,
		},
		{
			name:       "inside the write timeout",
			env:        map[string]string{"SERVICE_SERVER_HANDLER_TIMEOUT": "10s"},
			wantFailed: false,
		},
		{
			name:       "equal to the write timeout",
			env:        map[string]string{"SERVICE_SERVER_HANDLER_TIMEOUT": "30s"},
			wantFailed: true,
		},
		{
			name:       "beyond the write timeout",
			env:        map[string]string{"SERVICE_SERVER_HANDLER_TIMEOUT": "60s"},
			wantFailed: true,
		},
		{
			name:       "negative",
			env:        map[string]string{"SERVICE_SERVER_HANDLER_TIMEOUT": "-1s"},
			wantFailed: true,
		},
		{
			name:       "the administrative listener is held to the same rule",
			env:        map[string]string{"SERVICE_ADMIN_HANDLER_TIMEOUT": "45s"},
			wantFailed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Load(t.Context(), envconfig.MapLookuper(tt.env), "1.2.3")

			if tt.wantFailed {
				if err == nil {
					t.Fatalf("Load() error = nil, want a failure; got %+v", got)
				}

				if !strings.Contains(err.Error(), "HandlerTimeout") {
					t.Errorf("Load() error = %q, want it to name the failing field", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("Load() error = %v, want the handler timeout accepted", err)
			}
		})
	}
}

// TestListenAddressesNamesEachVariableFromItsTags covers AC-001.2.
//
// The names below are the ones README.md and .env.example tell an operator to
// export. They are derived from the struct tags rather than written into the
// validator, so this is what catches a renamed prefix or address field turning
// the startup message into advice about a variable that does not exist.
func TestListenAddressesNamesEachVariableFromItsTags(t *testing.T) {
	t.Parallel()

	cfg := Default()

	got, err := cfg.listenAddresses()
	if err != nil {
		t.Fatalf("listenAddresses() error = %v, want nil", err)
	}

	want := []listenAddress{
		{variable: "SERVICE_SERVER_ADDR", addr: cfg.Server.Addr},
		{variable: "SERVICE_ADMIN_ADDR", addr: cfg.Admin.Addr},
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(listenAddress{})); diff != "" {
		t.Errorf("listenAddresses() mismatch (-want +got):\n%s", diff)
	}
}

// TestLoadReportsMistakesInSeparateSettingsGroupsTogether covers AC-001.2.
//
// The trusted-CIDR list is checked by the proxy package's own hook and the
// listen ports by the hook over the composed configuration. Neither gates the
// other, so an operator writing an environment block from scratch is told
// about both at once instead of finding the second one after fixing the first
// and restarting.
func TestLoadReportsMistakesInSeparateSettingsGroupsTogether(t *testing.T) {
	t.Parallel()

	got, err := Load(t.Context(), envconfig.MapLookuper(map[string]string{
		"SERVICE_PROXY_TRUSTED_CIDRS": "not-a-cidr",
		"SERVICE_SERVER_ADDR":         "127.0.0.1:0",
	}), "1.2.3")
	if err == nil {
		t.Fatalf("Load() error = nil, want a failure; got %+v", got)
	}

	for _, want := range []string{"SERVICE_PROXY_", "SERVICE_SERVER_ADDR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error = %q, want it to name %s", err, want)
		}
	}
}
