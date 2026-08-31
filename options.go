package camunda

// Option configures a Config. Options are applied after environment resolution
// and therefore take precedence over environment variables.
type Option func(*Config)

// WithRestAddress sets the Orchestration Cluster REST base address.
func WithRestAddress(addr string) Option {
	return func(c *Config) { c.RestAddress = normalizeRestAddress(addr) }
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

// WithForceREST forces the pure-REST path even when the gateway advertises FALCON
// support (useful where WebSockets are blocked by a proxy).
func WithForceREST(force bool) Option {
	return func(c *Config) { c.ForceREST = force }
}
