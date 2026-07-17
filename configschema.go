package camunda

// ConfigField documents a single environment variable the SDK reads while
// resolving configuration. It carries the variable's alias precedence, default,
// and whether it holds credential material.
type ConfigField struct {
	// Keys are the environment variable names checked in precedence order; the
	// first non-empty value wins. The first entry is the canonical CAMUNDA_* name;
	// later entries are accepted aliases (e.g. legacy ZEEBE_* names).
	Keys []string
	// Default is the value applied when none of Keys is set. Empty means the field
	// has no built-in default (it stays unset / zero).
	Default string
	// Secret marks credential material that must be redacted in diagnostics.
	Secret bool
	// Description is a one-line human-readable summary.
	Description string
}

// ConfigSchema is the canonical registry of every environment variable the SDK
// consumes during configuration resolution (see loadConfig in config.go). It is
// the single source of truth for the SDK's configuration surface: it documents
// the accepted variables, their aliases, and their defaults, and it is kept in
// lock-step with the actual resolution code by TestConfigSchemaMatchesReads
// (configschema_test.go), which fails if a variable is read but unregistered or
// registered but never read.
//
// It intentionally mirrors the JS SDK's configSchema so the SDKs expose the same
// configuration contract.
var ConfigSchema = []ConfigField{
	{Keys: []string{"CAMUNDA_REST_ADDRESS", "ZEEBE_REST_ADDRESS"}, Default: defaultRestAddress, Description: "Orchestration Cluster REST base address."},
	{Keys: []string{"CAMUNDA_GRPC_ADDRESS", "ZEEBE_GRPC_ADDRESS"}, Default: defaultGrpcAddress, Description: "Zeebe gRPC gateway address (host:port) for the streaming job worker."},

	{Keys: []string{"CAMUNDA_AUTH_STRATEGY"}, Description: "Authentication strategy: OAUTH, BASIC, or NONE. Inferred from credentials when unset."},
	{Keys: []string{"CAMUNDA_CLIENT_ID", "ZEEBE_CLIENT_ID"}, Description: "OAuth 2.0 client id (client-credentials grant)."},
	{Keys: []string{"CAMUNDA_CLIENT_SECRET", "ZEEBE_CLIENT_SECRET"}, Secret: true, Description: "OAuth 2.0 client secret."},
	{Keys: []string{"CAMUNDA_OAUTH_URL", "ZEEBE_AUTHORIZATION_SERVER_URL"}, Description: "OAuth 2.0 token endpoint URL."},
	{Keys: []string{"CAMUNDA_TOKEN_AUDIENCE"}, Description: "OAuth token audience."},
	{Keys: []string{"CAMUNDA_TOKEN_SCOPE"}, Description: "OAuth token scope."},
	{Keys: []string{"CAMUNDA_OAUTH_CACHE_DIR"}, Description: "Directory for the on-disk OAuth token cache."},
	{Keys: []string{"CAMUNDA_BASIC_AUTH_USERNAME"}, Description: "HTTP Basic auth username."},
	{Keys: []string{"CAMUNDA_BASIC_AUTH_PASSWORD"}, Secret: true, Description: "HTTP Basic auth password."},

	{Keys: []string{"CAMUNDA_DEFAULT_TENANT_ID", "CAMUNDA_TENANT_ID"}, Description: "Default tenant id applied to operations that accept one."},

	{Keys: []string{"CAMUNDA_MTLS_CERT"}, Secret: true, Description: "Inline client certificate PEM for mutual TLS."},
	{Keys: []string{"CAMUNDA_MTLS_KEY"}, Secret: true, Description: "Inline client private key PEM for mutual TLS."},
	{Keys: []string{"CAMUNDA_MTLS_CA"}, Description: "Inline CA certificate PEM for verifying the server."},
	{Keys: []string{"CAMUNDA_MTLS_CERT_PATH"}, Description: "Path to the client certificate PEM for mutual TLS."},
	{Keys: []string{"CAMUNDA_MTLS_KEY_PATH"}, Description: "Path to the client private key PEM for mutual TLS."},
	{Keys: []string{"CAMUNDA_MTLS_CA_PATH"}, Description: "Path to the CA certificate PEM for verifying the server."},
	{Keys: []string{"CAMUNDA_MTLS_KEY_PASSPHRASE"}, Secret: true, Description: "Passphrase for an encrypted client private key."},

	{Keys: []string{"CAMUNDA_SDK_BACKPRESSURE_PROFILE"}, Default: "BALANCED", Description: "Adaptive backpressure profile: BALANCED (gates) or LEGACY (observe-only)."},
	{Keys: []string{"CAMUNDA_SDK_LOG_LEVEL"}, Default: "info", Description: "SDK log level: off/error/warn/info/debug/trace."},
	{Keys: []string{"CAMUNDA_SDK_EVENTUAL_POLL_DEFAULT_MS"}, Description: "Default eventual-consistency poll timeout, in milliseconds."},

	{Keys: []string{"CAMUNDA_SDK_HTTP_RETRY_MAX_ATTEMPTS"}, Description: "Max transient-error retry attempts."},
	{Keys: []string{"CAMUNDA_SDK_HTTP_RETRY_BASE_DELAY_MS"}, Description: "Base backoff delay for retries, in milliseconds."},
	{Keys: []string{"CAMUNDA_SDK_HTTP_RETRY_MAX_DELAY_MS"}, Description: "Max backoff delay for retries, in milliseconds."},

	{Keys: []string{"CAMUNDA_WORKER_TIMEOUT"}, Description: "Default job activation timeout, in milliseconds."},
	{Keys: []string{"CAMUNDA_WORKER_MAX_CONCURRENT_JOBS", "CAMUNDA_WORKER_MAX_JOBS_ACTIVE"}, Description: "Default max concurrently-activated jobs per worker."},
	{Keys: []string{"CAMUNDA_WORKER_REQUEST_TIMEOUT"}, Description: "Default activate-jobs request timeout, in milliseconds."},
	{Keys: []string{"CAMUNDA_WORKER_NAME"}, Default: defaultWorkerName, Description: "Default worker name."},
	{Keys: []string{"CAMUNDA_WORKER_STARTUP_JITTER_MAX_SECONDS"}, Description: "Max random startup delay for workers, in seconds."},
}
