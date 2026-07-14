# Command-line reference

The repository builds one binary, `llm-temporal-worker`. The production entry
point accepts a subcommand followed by flags:

```sh
llm-temporal-worker <command> [flags]
```

Every command that reads configuration accepts `--config PATH`. If it is not
provided, the binary reads `/etc/llmtw/config.yaml`.

## `help`

Print the command summary without reading configuration or starting runtime
dependencies. `help`, `-h`, and `--help` are equivalent when used as the first
argument:

```sh
llm-temporal-worker --help
```

The command exits with status `0`.

## `version`

Print the image and binary build metadata as JSON without reading configuration
or starting runtime dependencies:

```sh
llm-temporal-worker version
```

The result contains the version, revision, build time, Go version, and source
URL stamped into the binary. It contains no configuration or credentials. The
`make image-verify` gate compares this output with the final image labels.

## `health-server`

Start only the probe listener used by hardened-image verification:

```sh
llm-temporal-worker health-server --address 0.0.0.0:8080
```

It reports `/health/live` while its listener is running and keeps
`/health/ready` unavailable because it does not construct the worker or check
dependencies. It is not a production worker mode; deployment manifests must
continue to use `worker --config /etc/llmtw/config.yaml`.

## `worker`

Start the production composition, including the configured provider and state
backends, Temporal client, health and metrics listeners, and Activity worker:

```sh
llm-temporal-worker worker --config /etc/llmtw/config.yaml
```

The command validates the YAML before constructing runtime dependencies. It
then blocks while the worker polls its configured Temporal task queue. A
`SIGINT` or `SIGTERM` begins graceful shutdown: readiness is withdrawn, polling
stops, in-flight Activities receive their configured grace period, and the
runtime drains its snapshot clients before exiting.

The worker resolves referenced secrets and catalog files while constructing the
production runtime. A failure in that phase is fatal; the process must not
start polling with an incomplete provider or state configuration.

## `validate-config`

Parse and validate the strict YAML shape without starting the worker:

```sh
llm-temporal-worker validate-config --config ./config.yaml
```

On success it prints the effective configuration digest as a `config version`.
In the production binary this command performs schema/default validation only;
it does not read secret values, load catalog file contents, dial Temporal,
connect to Redis or S3, or contact a provider. Use `worker` startup as the
full dependency and reference-validation gate.

## `print-effective-config`

Print the canonical effective configuration as JSON without starting runtime
dependencies:

```sh
llm-temporal-worker print-effective-config --config ./config.yaml
```

The output contains configuration and secret-reference metadata, not resolved
secret values. It is canonical JSON rather than the source YAML, so it is
appropriate for comparing configuration snapshots but should still be handled
as deployment-sensitive material.

## `reconcile`

The command dispatcher recognizes:

```sh
llm-temporal-worker reconcile --operation-id OPERATION_ID
```

The production binary does not currently wire a reconciliation backend, so
this command exits with `reconcile backend is unavailable`. Do not use it as an
operations procedure yet. The flag and injected callback remain available for
tests and custom embeddings until the scoped production reconciliation
implementation is complete.

## Configuration reload

The internal application package supports an atomic reload API that validates a
replacement and keeps the prior snapshot when validation fails. The production
CLI does not currently install a `SIGHUP` handler or a file watcher. Operators
must therefore restart the worker to apply a changed configuration; a file
replacement alone does not reload a running process.

## Exit status and diagnostics

- `0` means the command completed successfully.
- `1` means a file, configuration, dependency, or runtime operation failed.
- `2` means the invocation was invalid, such as a missing command, unknown
  command, invalid flag, or missing `--operation-id`.

Command errors are reduced to a single safe line. Credential, token, prompt,
output, and provider-body terms are redacted from the CLI error boundary.
