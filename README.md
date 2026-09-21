# template-go-http-server

A Go service that serves a browser-based interface from a single binary.

## Using this template

This repository is a starting point, not a dependency. Nothing here imports it; a project
made from it owns every file and edits them freely.

### With gonew

[`gonew`](https://pkg.go.dev/golang.org/x/tools/cmd/gonew) copies the tree and rewrites the
module path and its imports in one step:

```sh
go run golang.org/x/tools/cmd/gonew@latest \
    github.com/uhdaptuhbl/template-go-http-server \
    github.com/you/your-service
cd your-service
go tool mage bootstrap
```

`gonew` rewrites the module path and the import paths that follow from it. It rewrites
nothing else, so the rename below still has to be done by hand.

### Standalone

Clone it, or use GitHub's "Use this template" button, then drop the history that is not
yours:

```sh
git clone git@github.com:uhdaptuhbl/template-go-http-server.git your-service
cd your-service
rm -rf .git && git init
go mod edit -module github.com/you/your-service
grep -rl github.com/uhdaptuhbl/template-go-http-server --exclude-dir=.git . \
  | xargs sed -i 's|github.com/uhdaptuhbl/template-go-http-server|github.com/you/your-service|g'
go tool mage bootstrap
```

`go mod edit` rewrites `go.mod` alone, which is why the `sed` is there: the import
statements naming the old path have to move with it or nothing compiles. `gonew` above does
both in one step, which is the only reason to prefer it.

### What to rename

The template names itself `service` throughout, which is deliberately generic so that a
forgotten rename is visible rather than plausible. Four substitutions cover it, and none of
them is derived from another:

| What | From | Where |
| --- | --- | --- |
| Module path | `github.com/uhdaptuhbl/template-go-http-server` | `go.mod`, every import, `.golangci.yaml`'s `depguard` allow prefix |
| Environment prefix | `SERVICE_` | `internal/config/config.go` struct tags, `.env.example`, `deploy/local/compose.yaml`, this file |
| Binary and image name | `service` | `cmd/service/`, `magefile.go`, both Dockerfiles, `.goreleaser.yaml`, `.github/workflows/` |
| Reported service name | `telemetry.DefaultServiceName` | `internal/telemetry/telemetry.go`, one constant |

```sh
grep -rn 'SERVICE_\|service' --exclude-dir=.git .
```

finds all of them. Prose in this file, `CLAUDE.md`, `docs/adr/`, and `specs/` describes the
template rather than your service, and is yours to rewrite.

### What to decide

- **`docs/adr/0002-frontend-stack.md` is open,** deliberately. It records four options for
  the frontend and the four things that must be settled to close it, including the JSON
  field-naming convention that `docs/adr/0010-json-api-response-conventions.md` defers to
  it. Accept it, or supersede it, before writing frontend code.
- **`specs/001-` through `specs/007-` describe this template's own behaviour,** not yours.
  They are worked examples of the three-document pattern `CLAUDE.md` requires, and their
  criteria are cited by the tests in this repository. Keep them, and number your first
  feature `008-`.
- **`CLAUDE.md` is the engineering constitution,** including the one deliberate departure
  from common Go idiom (one declaration per line). Read it before the first commit; change
  it if you disagree with it, rather than drifting from it silently.
- **The licence is Apache 2.0.** Your service's own code is yours to license as you like.

## Requirements

- Go 1.27 or later
- [GoReleaser](https://goreleaser.com) 2.x, only to rehearse or check a release
- Docker, only to build the image or run the local observability stack

golangci-lint and mage need no install: both are pinned as tools in `go.mod` and run through
`go tool`.

## Running

```sh
go run ./cmd/service
```

Two listeners start. The application listener on `:8080` serves users:

| Route | Purpose |
| --- | --- |
| `GET /healthz` | Liveness check; returns `{"status":"ok"}` |
| `GET /readyz` | Readiness check; `200 {"status":"ready"}` while serving, `503 {"status":"not_ready","failing":["<check>"]}` when a registered dependency check fails, `503 {"status":"draining"}` once shutdown has begun |
| `GET /` | The embedded frontend |

Every response from this listener carries `X-Content-Type-Options: nosniff`,
`X-Frame-Options: DENY`, and `Content-Security-Policy: frame-ancestors 'none'`; the frontend
handler replaces the last with the fuller policy its assets need.

The administrative listener on `127.0.0.1:9090` serves operators:

| Route | Purpose |
| --- | --- |
| `GET /metrics` | Prometheus exposition, including Go runtime metrics |
| `GET /healthz` | The same liveness check, so this port is sufficient alone |
| `GET /readyz` | The same readiness check, with the same three answers |
| `GET /version` | The running build: version, commit, build date, Go version, and process start time |
| `GET /debug/pprof/` | Runtime profiling |

`GET /version` answers what the start-up log entry said, for when that line has rotated
away:

```console
$ curl -s 127.0.0.1:9090/version
{"version":"1.4.0","commit":"a1b2c3d","build_date":"2026-09-19T08:11:04Z","go_version":"go1.27.1","started_at":"2026-09-19T08:14:22Z"}
```

It is deliberately absent from the application listener: naming the exact release tells
anyone who can reach it which vulnerabilities to try. The same values are also labels on the
`build_info` metric, for tooling that would rather join on them than parse JSON.

The administrative listener binds loopback by default. Reaching it from elsewhere requires
setting `SERVICE_ADMIN_ADDR`, and doing so requires a network policy, because the profiling
endpoints will dump the heap, every goroutine's stack, and the process command line to
anyone who can reach them.

Readiness consults nothing by default. A dependency the process cannot serve without is
registered in `cmd/service/main.go` with `ready.AddCheck("database", db.PingContext)`; every
probe then runs each check concurrently under a one-second deadline and answers `not_ready`,
naming the failing checks, until they pass. The check's error goes to the log, not the
response.

On shutdown, readiness starts failing first, so a load balancer or orchestrator stops
sending new traffic. The process keeps serving for `SERVICE_LIFECYCLE_PRE_DRAIN_DELAY`, to
give that signal time to propagate, then the listeners stop accepting new connections and
in-flight requests get up to `SHUTDOWN_TIMEOUT` to finish. A second `SIGINT` or `SIGTERM`
during this window abandons in-flight requests and exits with status 1. If either listener
fails to start, the process stops rather than running half of what was asked for.

### Configuration

Every setting is read from the environment, over a default that works unconfigured. A value
that fails validation stops the process at startup and names the field, rather than surfacing
later as odd behaviour. See `docs/adr/0006-configuration-loading.md`.

`.env.example` lists every variable below at its default, ready to copy to `.env`. Nothing in
the process reads that file: it is loaded by whatever runs the binary, such as
`docker run --env-file`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `SERVICE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SERVICE_LOG_FORMAT` | `console` | `console` or `json` |
| `SERVICE_SERVER_ADDR` | `:8080` | Application listener, `host:port` |
| `SERVICE_ADMIN_ADDR` | `127.0.0.1:9090` | Administrative listener, `host:port` |
| `SERVICE_LIFECYCLE_PRE_DRAIN_DELAY` | `5s` | Time between readiness failing and listeners closing |
| `SERVICE_PROXY_TRUSTED_CIDRS` | (empty) | Comma-separated CIDRs whose forwarded headers are believed |
| `SERVICE_SERVER_MAX_HEADER_BYTES` | `1MiB` | Request line plus headers ceiling |
| `SERVICE_SERVER_MAX_REQUEST_BODY_BYTES` | `1MiB` | Request body ceiling |
| `SERVICE_SERVER_MAX_IN_FLIGHT` | `0` | Concurrent request cap; 0 disables |
| `SERVICE_SERVER_HANDLER_TIMEOUT` | `0s` | Per-request handler deadline; 0 disables, otherwise shorter than `WRITE_TIMEOUT` |

Every `SERVICE_SERVER_*` row above has a `SERVICE_ADMIN_*` counterpart that configures the
administrative listener the same way. `SERVICE_ADMIN_ADDR` is listed separately only because
its default differs.

Both listeners also accept `READ_HEADER_TIMEOUT`, `READ_TIMEOUT`, `WRITE_TIMEOUT`,
`IDLE_TIMEOUT`, and `SHUTDOWN_TIMEOUT` under their own prefix, each a Go duration such as
`30s`. They are tuned to release resources from stalled clients without interrupting ordinary
requests, so changing them is rarely necessary. `READ_TIMEOUT` bounds the whole request and so
must be at least `READ_HEADER_TIMEOUT`; the process refuses to start otherwise.

A duration needs its unit: `30` is refused, because it could mean either seconds or
milliseconds depending on who is reading. A size is a plain count of bytes, or a count with a
suffix, binary (`KiB`, `MiB`, `GiB`, `TiB`) or decimal (`KB`, `MB`, `GB`, `TB`). Both render
in the same notation in the configuration the process logs at startup, so a value read out of
a log can be pasted back into the environment unchanged.

A listen address is `host:port`, where the host may be omitted to bind every interface, or be
a hostname, an IPv4 literal, or a bracketed IPv6 literal such as `[::1]:9090`. Port 0 is
refused: it asks the kernel for a port that nothing can be told to route to.

A variable that is set but empty counts as unset, so an exported-but-empty variable cannot
blank a listen address.

Log output is never sampled. Every served request produces one entry at any request rate,
which is what makes the request log usable for accounting rather than just for spot checks.
Dropping entries under load is a decision for the log pipeline, which can see the whole
stream; a process that samples from inside a single message bucket discards exactly the
traffic that is most worth keeping.

Telemetry is configured through the standard OpenTelemetry variables instead, without this
project's prefix, because the OpenTelemetry specification fixes those names.

### Calling another service

`internal/httpclient` builds outbound HTTP clients. Use it rather than `http.Get` or
`http.DefaultClient`: those have no timeout, share one connection pool with everything else
in the process, and send no trace context, so a slow dependency holds the inbound request
that triggered it and the two halves of one user action land in two traces.

```go
client, err := httpclient.New(httpclient.Default(), httpclient.Telemetry{
	TracerProvider: tracerProvider,
	MeterProvider:  meterProvider,
	Propagator:     propagator,
})
```

What comes back is an ordinary `*http.Client`, so it can be handed to any SDK that takes
one. Build one per dependency rather than one for the process: the pool limits that suit a
dependency are a property of that dependency, and a shared pool exhausted by one peer
starves the rest.

Requests honour `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY` on the same terms the standard
library's default transport does, so a service deployed behind an egress gateway needs no
change here.

The client does not retry, back off, or open a circuit. Retrying is safe only for a call
the caller knows to be idempotent, which is knowledge the call site has and the client does
not.

`httpclient.Config` carries the timeouts and pool limits, each with an `env` tag and no
prefix of its own. A service composes it into its configuration under whatever prefix names
the dependency, and the settings then appear in the effective configuration logged at
start-up like any other:

```go
type Config struct {
	Billing httpclient.Config `env:", prefix=SERVICE_BILLING_" json:"billing"`
}
```

That declares `SERVICE_BILLING_TIMEOUT`, `SERVICE_BILLING_CONNECT_TIMEOUT`,
`SERVICE_BILLING_TLS_HANDSHAKE_TIMEOUT`, `SERVICE_BILLING_RESPONSE_HEADER_TIMEOUT`,
`SERVICE_BILLING_MAX_CONNS_PER_HOST`, `SERVICE_BILLING_MAX_IDLE_CONNS`,
`SERVICE_BILLING_MAX_IDLE_CONNS_PER_HOST`, and `SERVICE_BILLING_IDLE_CONN_TIMEOUT`.
Defaults are on the `Default*` constants in the package. No variable exists until a service
composes the struct, which is why none is listed in the table above.

### Tracing

Tracing is off until a collector is configured, through the standard OpenTelemetry variables:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 OTEL_SERVICE_NAME=service go run ./cmd/service
```

`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` names a traces-only collector and takes precedence
over the general endpoint when both are set, as the OpenTelemetry specification requires.
With no endpoint set, the tracing pipeline hands back a no-op tracer provider and
instrumentation costs a few comparisons per request. Providers are passed to the HTTP
instrumentation explicitly through `server.Telemetry` rather than read from OpenTelemetry's
process-wide globals. Inbound `traceparent` headers are still read and propagated either
way, so this process is never the hop where a distributed trace breaks. Request log lines
carry `trace_id` and `span_id` when a span is active. See
`docs/adr/0004-use-opentelemetry-for-tracing.md`.

### Local observability

```sh
docker compose -f deploy/local/compose.yaml up --build
```

builds the image and runs it behind an nginx gateway, beside Jaeger and Prometheus, wired
together: the service exports traces to Jaeger over OTLP/HTTP, and Prometheus scrapes the
administrative listener, which is bound to all interfaces inside the Compose network so it
can be reached by service name. Traces are at `http://localhost:16686`, metrics at
`http://localhost:9091`, the service at `http://localhost:8080`. Every published port is
bound to loopback on the host, so the stack is not offered to any network the machine
happens to be on; inside the Compose network the containers still reach each other by name.

The gateway is there because `docs/adr/0007-deployment-topology.md` says the service is
always deployed behind a reverse proxy, and everything downstream of that decision, the
forwarded client address and the correlation ID, has no way to be wrong locally if nothing
local sets those headers. `http://localhost:8080` is the gateway; the application listener
is not published, so the direct path the ADR rules out is not the one a developer
bookmarks. `deploy/local/nginx.conf` is a starting point for a real proxy configuration:
no TLS, no redirect, no HSTS, no compression, all of which belong to the production proxy,
but the `proxy_set_header` block carries over unchanged, and the comments there explain why
`X-Forwarded-For` is replaced rather than appended. `SERVICE_PROXY_TRUSTED_CIDRS` names the
gateway's address alone, which is what makes the service believe those headers. See
`specs/007-local-gateway/`.

This is a developer convenience, not a deployment artifact.

## Deployment

This service must run behind a TLS-terminating reverse proxy. It is not safe to expose directly
to the internet. See `docs/adr/0007-deployment-topology.md`.

Kubernetes probes point at the application listener on port 8080:

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
readinessProbe:
  httpGet:
    path: /readyz
    port: 8080
```

`terminationGracePeriodSeconds` must exceed `SERVICE_LIFECYCLE_PRE_DRAIN_DELAY` plus
`SERVICE_SERVER_SHUTDOWN_TIMEOUT`, or the kubelet will `SIGKILL` the process mid-drain.

An nginx `location` block forwards the client address and a request ID:

```nginx
location / {
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Request-Id $request_id;
    proxy_pass http://127.0.0.1:8080;
}
```

Neither header is believed unless the proxy's address is listed in
`SERVICE_PROXY_TRUSTED_CIDRS`. Every entry must be a network rather than a bare address, so
`10.0.0.0/8` and `2001:db8::/32` are accepted and `10.0.0.1` is not. An entry that does not
parse stops the process at startup: a typo in a trust boundary must not silently narrow it.
Empty entries, which a trailing or doubled comma leaves behind, are ignored.

Compression and HSTS are the proxy's job and are deliberately absent from the binary.

## Building

```sh
go tool mage -l
```

lists the available targets. No global install is needed: the mage version is pinned by the
tool directive in `go.mod`. The targets are:

| Target | Purpose |
| --- | --- |
| `Bootstrap` | Prepares a fresh clone: downloads requirements, installs the pinned tools, copies `.env.example` to `.env`, and runs lint and the unit suite once |
| `Build` | Compiles the server into `bin/service` with version information stamped in |
| `Test` | Runs the unit test suite |
| `Race` | Runs the unit test suite under the race detector, the form CI gates on |
| `Integration` | Runs the build-tagged integration suite |
| `Lint` | Runs the `go.mod`-pinned golangci-lint against the repository root configuration, then again over the build-tagged integration suite |
| `Cover` | Runs the test suite with coverage and reports the per-function summary |
| `Vuln` | Scans dependencies and the standard library for known vulnerabilities |
| `Tidy` | Prunes and refreshes the module requirements |
| `Docker` | Builds the container image |
| `ReleaseCheck` | Validates `.goreleaser.yaml` without building anything |
| `Snapshot` | Builds both release architectures and their images locally, publishing nothing |
| `Run` | Builds and starts the server with the current working configuration |
| `Clean` | Removes build and coverage output |

`go tool mage docker` builds the container image. The admin listener binds loopback inside
the container too, so scraping it from outside requires setting `SERVICE_ADMIN_ADDR`.

## Releases

Pushing a tag of the form `vX.Y.Z` publishes a multi-architecture image to
`ghcr.io/uhdaptuhbl/service`, tagged `X.Y.Z`, `X.Y`, `X`, and `latest`, with release notes
generated from the commit subjects since the previous tag. Nothing else is published: the
image is the artifact, and `docs/adr/0007-deployment-topology.md` says why there is no loose
binary beside it.

Rehearse first. The release workflow's next chance to fail is otherwise the tag itself:

```sh
go tool mage releasecheck   # the configuration parses
go tool mage snapshot       # both architectures and both images, published nowhere
git tag -a v1.2.3 -m "v1.2.3" && git push origin v1.2.3
```

A snapshot builds one image per architecture with a platform suffix rather than a single
manifest, because buildx cannot load a multi-platform manifest into a local daemon.

The same lint, race, and vulnerability gates CI applies to a branch run again on the tag,
before anything is published. A tag that does not match `v*.*.*` starts nothing.

### Verifying an image

Every published image is signed with a keyless Sigstore signature whose identity is this
repository's release workflow, so verification asks "which workflow in which repository built
this" rather than "do I have the right public key". There is no key to distribute:

```sh
cosign verify ghcr.io/uhdaptuhbl/service:1.2.3 \
  --certificate-identity-regexp '^https://github.com/uhdaptuhbl/template-go-http-server/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Public repositories additionally carry a build provenance attestation:

```sh
gh attestation verify oci://ghcr.io/uhdaptuhbl/service:1.2.3 --repo uhdaptuhbl/template-go-http-server
```

This repository is private, where GitHub does not issue attestations outside Enterprise
Cloud, so that step is skipped here and runs unchanged in a public project built from this
one. Signing is not affected. See `specs/004-release-automation/`.

## Development

```sh
go tool mage race
go tool mage lint
go tool mage integration
```

All three must pass before a change is considered done; they are the same commands CI
gates on. `CONTRIBUTING.md` covers setup and how a change is shaped, and `CLAUDE.md` holds
the full definition of done.

## Layout

```
cmd/service/         Entrypoint: wiring only, no business logic
internal/buildinfo/  The binary's build identity, stamped in at link time
internal/config/     Environment decoding, validation, and secret redaction
internal/configtype/ Setting types that read and render the way an operator writes them
internal/httpclient/ Outbound HTTP clients: bounded, pooled per dependency, traced
internal/logging/    Zap logger construction and the slog-to-zap bridge
internal/server/     HTTP listeners, routing, middleware, graceful shutdown
internal/signals/    Second-signal handling during shutdown
internal/telemetry/  OpenTelemetry tracing and metrics setup
internal/web/        Embeds and serves the built frontend
deploy/local/        Compose stack: the service behind a gateway, with traces and metrics
docs/adr/            Architecture decision records
specs/               Per-feature requirements, design, and tasks
```

A feature route lives in `internal/server` or a package beside it and is registered in
`NewMux`. `server.DecodeJSON` reads its request, `server.WriteJSON` writes its response,
and `server.WriteError` turns an error into the one error body every endpoint shares: a
top-level `errors` array whose members borrow JSON:API's names, described in
`docs/adr/0010-json-api-response-conventions.md`. A handler that wants the client to learn
anything about a failure returns a `server.ClientError`; `errors.Join` several of them and
each becomes its own error object, so a validation failure reports every bad field at once.
Any other error is a 500 whose cause reaches the log and never the client.

There is no frontend source tree yet. `CLAUDE.md` fixes its location as `ui/`;
`docs/adr/0002-frontend-stack.md` is still open and decides the stack that goes there.
Whatever that turns out to be, its build writes into `internal/web/dist/` rather than a
`dist/` beside the source, because `go:embed` cannot reference paths outside its own package
directory.

## Documentation

- `CLAUDE.md`: engineering standards, development model, and definition of done
- `CONTRIBUTING.md`: setup, the gates to run, and commit conventions
- `CODE_OF_CONDUCT.md`: the Contributor Covenant, and how to report a problem
- `SECURITY.md`: how to report a vulnerability, and what is in scope
- `.env.example`: every configuration variable at its default
- `docs/adr/`: architecture decisions, including open ones recorded as `proposed`
- `specs/templates/`: the three documents each feature carries

## License

Apache License 2.0. See [LICENSE](LICENSE). A project started from this template may
relicense its own work; the template imposes no restriction beyond Apache 2.0's terms.
