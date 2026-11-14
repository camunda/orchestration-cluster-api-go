package camunda

import "net/http"

// Option configures a Config. Options are applied after environment resolution
// and therefore take precedence over environment variables.
type Option func(*Config)

// WithRestAddress sets the Orchestration Cluster REST base address.
func WithRestAddress(addr string) Option {
	return func(c *Config) { c.RestAddress = normalizeRestAddress(addr) }
}

// WithExactRestAddress controls whether the SDK uses RestAddress exactly as
// provided. When exact is true, the automatic /v2 suffix is not appended — the
// address is used verbatim. This is intended for deployments behind an API
// gateway / reverse proxy whose base path does not follow the .../v2 convention.
// It maps to the CAMUNDA_REST_ADDRESS_EXACT environment variable.
func WithExactRestAddress(exact bool) Option {
	return func(c *Config) { c.ExactRestAddress = exact }
}

// WithGrpcAddress sets the Zeebe gRPC gateway address (host:port) used by the
// gRPC streaming job worker.
func WithGrpcAddress(addr string) Option {
	return func(c *Config) { c.GrpcAddress = addr }
}

// WithOAuth selects the OAuth 2.0 client-credentials strategy with the given
// client id, secret, and token endpoint URL.
func WithOAuth(clientID, clientSecret, tokenURL string) Option {
	return func(c *Config) {
		c.AuthStrategy = AuthOAuth
		c.ClientID = clientID
		c.ClientSecret = clientSecret
		c.OAuthURL = tokenURL
	}
}

// WithOAuthAudience sets the OAuth token audience.
func WithOAuthAudience(audience string) Option {
	return func(c *Config) { c.TokenAudience = audience }
}

// WithOAuthScope sets the OAuth token scope.
func WithOAuthScope(scope string) Option {
	return func(c *Config) { c.OAuthScope = scope }
}

// WithOAuthCacheDir enables the on-disk OAuth token cache at dir.
func WithOAuthCacheDir(dir string) Option {
	return func(c *Config) { c.OAuthCacheDir = dir }
}

// WithBasicAuth selects HTTP Basic authentication with the given credentials.
func WithBasicAuth(username, password string) Option {
	return func(c *Config) {
		c.AuthStrategy = AuthBasic
		c.BasicAuthUsername = username
		c.BasicAuthPassword = password
	}
}

// WithNoAuth selects the no-authentication strategy (e.g. local development).
func WithNoAuth() Option {
	return func(c *Config) { c.AuthStrategy = AuthNone }
}

// WithBackpressureProfile sets the adaptive backpressure profile.
func WithBackpressureProfile(p BackpressureProfile) Option {
	return func(c *Config) { c.BackpressureProfile = p }
}

// WithLogLevel sets the SDK log level.
func WithLogLevel(l LogLevel) Option {
	return func(c *Config) { c.LogLevel = l }
}

// WithRetry sets the transient-error retry policy.
func WithRetry(rc RetryConfig) Option {
	return func(c *Config) { c.Retry = rc }
}

// WithDefaultTenantID sets the default tenant id applied to operations that
// accept one.
func WithDefaultTenantID(id string) Option {
	return func(c *Config) { c.DefaultTenantID = id }
}

// WithFalcon enables or disables the FALCON (nanobpmn command-stream) transport
// upgrade. It is enabled by default and only engages when the gateway advertises
// FALCON support; against stock Camunda the SDK stays on REST regardless.
func WithFalcon(enabled bool) Option {
	return func(c *Config) { c.Falcon = enabled }
}

// WithClock sets the clock the client will resolve cadence through. Defaults to
// [LiveClock].
//
// Runtime call sites are being migrated onto the injected clock (see
// camunda/orchestration-cluster-api-go#40); until that lands the clock is stored and
// reachable via CamundaClient.Clock, but retry backoff, the backpressure gate, token
// refresh, worker polling and consistency polling still use real time.
//
// A nil Clock selects the default. A *typed* nil -- a nil pointer boxed in a non-nil
// interface, such as (*myClock)(nil) -- is rejected by [New] with a configuration
// error instead: unlike an untyped nil it claims to be a usable clock, and would
// panic on first use deep inside the runtime.
func WithClock(c Clock) Option {
	return func(cfg *Config) { cfg.Clock = c }
}

// WithRandom sets the source of randomness the client draws jitter from: retry
// backoff, worker startup delay and FALCON endpoint selection. Defaults to
// [LiveRandom]. Pass a [SeededRandom] to make those draws reproducible.
//
// A nil Random selects the default; a typed nil is rejected by [New], as for
// [WithClock].
func WithRandom(r Random) Option {
	return func(cfg *Config) { cfg.Random = r }
}

// WithForceREST forces the pure-REST path even when the gateway advertises FALCON
// support (useful where WebSockets are blocked by a proxy).
func WithForceREST(force bool) Option {
	return func(c *Config) { c.ForceREST = force }
}

// WithMaxIdleConns sets the maximum number of idle (keep-alive) connections the
// SDK's HTTP transport keeps open across all hosts. It maps to the
// CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS environment variable and applies only when the
// SDK builds the base transport itself (not with [WithHTTPClient] or
// [WithTransport]).
//
// The SDK default (256) deliberately replaces Go's http.DefaultTransport pool,
// whose 2 idle connections per host force a job worker with many jobs in flight
// to open and close a TCP connection per call — churning sockets into TIME_WAIT
// and exhausting ephemeral ports (camunda/orchestration-cluster-api-go#162).
func WithMaxIdleConns(n int) Option {
	return func(c *Config) { c.MaxIdleConns = n }
}

// WithMaxIdleConnsPerHost sets the maximum number of idle (keep-alive)
// connections the SDK's HTTP transport keeps open per host. It maps to the
// CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS_PER_HOST environment variable and applies only
// when the SDK builds the base transport itself (not with [WithHTTPClient] or
// [WithTransport]). Size it to at least the sum of your workers'
// WithMaxConcurrentJobs plus their pollers when running many workers against one
// gateway.
func WithMaxIdleConnsPerHost(n int) Option {
	return func(c *Config) { c.MaxIdleConnsPerHost = n }
}

// WithHTTPClient supplies the *http.Client used for all REST calls. The client
// is used as-is: the SDK's retry, backpressure, auth, and TLS layers are
// bypassed, so the supplied client must carry everything the deployment needs.
// Mutually exclusive with [WithTransport]; setting both fails [New] with a
// configuration error.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Config) { c.HTTPClient = client }
}

// WithTransport replaces the base http.RoundTripper at the bottom of the SDK's
// transport chain (backpressure -> retry -> auth -> Transport), so the SDK's
// resilience layers still apply on top of it. TLS material is not applied to a
// user-supplied transport — configure it on the transport itself. Mutually
// exclusive with [WithHTTPClient]; setting both fails [New] with a
// configuration error.
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Config) { c.Transport = rt }
}
