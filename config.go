package camunda

import (
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Default configuration values.
const (
	defaultRestAddress         = "http://localhost:8080"
	defaultGrpcAddress         = "localhost:26500"
	defaultEventualPollDefault = 5 * time.Second
	defaultRetryMaxAttempts    = 4
	defaultRetryBaseDelay      = 100 * time.Millisecond
	defaultRetryMaxDelay       = 5 * time.Second
	defaultWorkerTimeoutMs     = 60_000
	defaultWorkerRequestMs     = 10_000
	defaultWorkerMaxConcurrent = 10
	defaultWorkerName          = "go-sdk-worker"
)

// AuthStrategy selects the authentication mechanism.
type AuthStrategy int

// Authentication strategies.
const (
	AuthNone AuthStrategy = iota
	AuthBasic
	AuthOAuth
)

func (s AuthStrategy) String() string {
	switch s {
	case AuthBasic:
		return "BASIC"
	case AuthOAuth:
		return "OAUTH"
	default:
		return "NONE"
	}
}

// ParseAuthStrategy parses a CAMUNDA_AUTH_STRATEGY value.
func ParseAuthStrategy(s string) (AuthStrategy, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "OAUTH":
		return AuthOAuth, nil
	case "BASIC":
		return AuthBasic, nil
	case "NONE", "":
		return AuthNone, nil
	default:
		return AuthNone, configErrorf("unknown CAMUNDA_AUTH_STRATEGY %q (expected OAUTH, BASIC or NONE)", s)
	}
}

// BackpressureProfile selects the backpressure controller behavior.
type BackpressureProfile int

// Backpressure profiles.
const (
	ProfileBalanced BackpressureProfile = iota
	ProfileLegacy
)

func (p BackpressureProfile) String() string {
	if p == ProfileLegacy {
		return "LEGACY"
	}
	return "BALANCED"
}

// ParseBackpressureProfile parses a CAMUNDA_SDK_BACKPRESSURE_PROFILE value.
func ParseBackpressureProfile(s string) (BackpressureProfile, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "BALANCED", "":
		return ProfileBalanced, nil
	case "LEGACY":
		return ProfileLegacy, nil
	default:
		return ProfileBalanced, configErrorf("invalid CAMUNDA_SDK_BACKPRESSURE_PROFILE %q (expected BALANCED or LEGACY)", s)
	}
}

// LogLevel controls SDK log verbosity.
type LogLevel int

// Log levels.
const (
	LogOff LogLevel = iota
	LogError
	LogWarn
	LogInfo
	LogDebug
	LogTrace
)

func (l LogLevel) String() string {
	switch l {
	case LogOff:
		return "off"
	case LogError:
		return "error"
	case LogWarn:
		return "warn"
	case LogDebug:
		return "debug"
	case LogTrace:
		return "trace"
	default:
		return "info"
	}
}

// ParseLogLevel parses a CAMUNDA_SDK_LOG_LEVEL value.
func ParseLogLevel(s string) (LogLevel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none", "silent":
		return LogOff, nil
	case "error":
		return LogError, nil
	case "warn", "warning":
		return LogWarn, nil
	case "", "info":
		return LogInfo, nil
	case "debug":
		return LogDebug, nil
	case "trace", "verbose", "silly":
		return LogTrace, nil
	default:
		return LogInfo, configErrorf("unknown CAMUNDA_SDK_LOG_LEVEL %q (expected off/error/warn/info/debug/trace)", s)
	}
}

// RetryConfig is the transient-error HTTP retry policy.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// TLSConfig holds TLS / mutual-TLS material. Inline PEM values take precedence
// over the *Path file locations.
type TLSConfig struct {
	Cert          string
	Key           string
	CA            string
	CertPath      string
	KeyPath       string
	CAPath        string
	KeyPassphrase string
}

// IsConfigured reports whether any TLS material has been supplied.
func (t TLSConfig) IsConfigured() bool {
	return t.Cert != "" || t.Key != "" || t.CA != "" ||
		t.CertPath != "" || t.KeyPath != "" || t.CAPath != ""
}

// WorkerDefaults holds default job-worker settings sourced from CAMUNDA_WORKER_*.
type WorkerDefaults struct {
	TimeoutMs               int64
	MaxConcurrentJobs       int
	RequestTimeoutMs        int64
	Name                    string
	StartupJitterMaxSeconds int
}

// Config is the resolved SDK configuration.
type Config struct {
	RestAddress string
	GrpcAddress string

	AuthStrategy      AuthStrategy
	ClientID          string
	ClientSecret      string
	OAuthURL          string
	TokenAudience     string
	OAuthScope        string
	OAuthCacheDir     string
	BasicAuthUsername string
	BasicAuthPassword string

	DefaultTenantID string

	// Falcon enables the FALCON (nanobpmn command-stream) transport upgrade when
	// the gateway advertises it (CAMUNDA_FALCON, default true). ForceREST forces
	// the pure-REST path even when FALCON is advertised (CAMUNDA_FORCE_REST),
	// e.g. where WebSockets are blocked. Use FalconEnabled for the resolved state.
	Falcon    bool
	ForceREST bool

	BackpressureProfile BackpressureProfile
	LogLevel            LogLevel
	EventualPollDefault time.Duration

	Retry          RetryConfig
	TLS            TLSConfig
	WorkerDefaults WorkerDefaults

	// Clock resolves runtime cadence. Nil selects [LiveClock].
	Clock Clock
}

// LoadConfig resolves configuration from environment variables, applies opts
// (which take precedence over the environment), and validates the result.
func LoadConfig(opts ...Option) (*Config, error) {
	return loadConfig(os.Getenv, nil, opts...)
}

// loadConfig is the testable core of LoadConfig. getenv looks up an environment
// variable; overrides (highest precedence below opts) simulate env for tests.
func loadConfig(getenv func(string) string, overrides map[string]string, opts ...Option) (*Config, error) {
	cfg, err := resolveFromEnv(getenv, overrides)
	if err != nil {
		return nil, err
	}
	for _, opt := range opts {
		opt(cfg)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func resolveFromEnv(getenv func(string) string, overrides map[string]string) (*Config, error) {
	get := func(keys ...string) string {
		for _, k := range keys {
			if overrides != nil {
				if v, ok := overrides[k]; ok && v != "" {
					return v
				}
			}
			if v := getenv(k); v != "" {
				return v
			}
		}
		return ""
	}

	cfg := &Config{
		RestAddress:         normalizeRestAddress(orDefault(get("CAMUNDA_REST_ADDRESS", "ZEEBE_REST_ADDRESS"), defaultRestAddress)),
		GrpcAddress:         orDefault(get("CAMUNDA_GRPC_ADDRESS", "ZEEBE_GRPC_ADDRESS"), defaultGrpcAddress),
		ClientID:            get("CAMUNDA_CLIENT_ID", "ZEEBE_CLIENT_ID"),
		ClientSecret:        get("CAMUNDA_CLIENT_SECRET", "ZEEBE_CLIENT_SECRET"),
		OAuthURL:            get("CAMUNDA_OAUTH_URL", "ZEEBE_AUTHORIZATION_SERVER_URL"),
		TokenAudience:       get("CAMUNDA_TOKEN_AUDIENCE"),
		OAuthScope:          get("CAMUNDA_TOKEN_SCOPE"),
		OAuthCacheDir:       get("CAMUNDA_OAUTH_CACHE_DIR"),
		BasicAuthUsername:   get("CAMUNDA_BASIC_AUTH_USERNAME"),
		BasicAuthPassword:   get("CAMUNDA_BASIC_AUTH_PASSWORD"),
		DefaultTenantID:     get("CAMUNDA_DEFAULT_TENANT_ID", "CAMUNDA_TENANT_ID"),
		EventualPollDefault: defaultEventualPollDefault,
		TLS: TLSConfig{
			Cert:          get("CAMUNDA_MTLS_CERT"),
			Key:           get("CAMUNDA_MTLS_KEY"),
			CA:            get("CAMUNDA_MTLS_CA"),
			CertPath:      get("CAMUNDA_MTLS_CERT_PATH"),
			KeyPath:       get("CAMUNDA_MTLS_KEY_PATH"),
			CAPath:        get("CAMUNDA_MTLS_CA_PATH"),
			KeyPassphrase: get("CAMUNDA_MTLS_KEY_PASSPHRASE"),
		},
	}

	// Auth strategy: explicit value, else inferred from supplied credentials.
	if raw := get("CAMUNDA_AUTH_STRATEGY"); raw != "" {
		s, err := ParseAuthStrategy(raw)
		if err != nil {
			return nil, err
		}
		cfg.AuthStrategy = s
	} else {
		cfg.AuthStrategy = inferAuthStrategy(cfg)
	}

	profile, err := ParseBackpressureProfile(get("CAMUNDA_SDK_BACKPRESSURE_PROFILE"))
	if err != nil {
		return nil, err
	}
	cfg.BackpressureProfile = profile

	level, err := ParseLogLevel(get("CAMUNDA_SDK_LOG_LEVEL"))
	if err != nil {
		return nil, err
	}
	cfg.LogLevel = level

	// FALCON (nanobpmn command-stream) transport toggles. Enabled by default;
	// disabled by an explicitly falsy CAMUNDA_FALCON or a truthy CAMUNDA_FORCE_REST.
	if raw := get("CAMUNDA_FALCON"); raw == "" {
		cfg.Falcon = true
	} else {
		cfg.Falcon = !isFalsy(raw)
	}
	cfg.ForceREST = isTruthy(get("CAMUNDA_FORCE_REST"))

	if ms, err := parseMillis(get("CAMUNDA_SDK_EVENTUAL_POLL_DEFAULT_MS"), "CAMUNDA_SDK_EVENTUAL_POLL_DEFAULT_MS"); err != nil {
		return nil, err
	} else if ms > 0 {
		cfg.EventualPollDefault = time.Duration(ms) * time.Millisecond
	}

	cfg.Retry, err = resolveRetry(get)
	if err != nil {
		return nil, err
	}
	cfg.WorkerDefaults, err = resolveWorkerDefaults(get)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func resolveRetry(get func(...string) string) (RetryConfig, error) {
	rc := RetryConfig{MaxAttempts: defaultRetryMaxAttempts, BaseDelay: defaultRetryBaseDelay, MaxDelay: defaultRetryMaxDelay}
	if v, err := parseInt(get("CAMUNDA_SDK_HTTP_RETRY_MAX_ATTEMPTS"), "CAMUNDA_SDK_HTTP_RETRY_MAX_ATTEMPTS"); err != nil {
		return rc, err
	} else if v > 0 {
		rc.MaxAttempts = v
	}
	if ms, err := parseMillis(get("CAMUNDA_SDK_HTTP_RETRY_BASE_DELAY_MS"), "CAMUNDA_SDK_HTTP_RETRY_BASE_DELAY_MS"); err != nil {
		return rc, err
	} else if ms > 0 {
		rc.BaseDelay = time.Duration(ms) * time.Millisecond
	}
	if ms, err := parseMillis(get("CAMUNDA_SDK_HTTP_RETRY_MAX_DELAY_MS"), "CAMUNDA_SDK_HTTP_RETRY_MAX_DELAY_MS"); err != nil {
		return rc, err
	} else if ms > 0 {
		rc.MaxDelay = time.Duration(ms) * time.Millisecond
	}
	return rc, nil
}

func resolveWorkerDefaults(get func(...string) string) (WorkerDefaults, error) {
	wd := WorkerDefaults{
		TimeoutMs:         defaultWorkerTimeoutMs,
		MaxConcurrentJobs: defaultWorkerMaxConcurrent,
		RequestTimeoutMs:  defaultWorkerRequestMs,
		Name:              defaultWorkerName,
	}
	if v, err := parseMillis(get("CAMUNDA_WORKER_TIMEOUT"), "CAMUNDA_WORKER_TIMEOUT"); err != nil {
		return wd, err
	} else if v > 0 {
		wd.TimeoutMs = v
	}
	if v, err := parseInt(get("CAMUNDA_WORKER_MAX_CONCURRENT_JOBS", "CAMUNDA_WORKER_MAX_JOBS_ACTIVE"), "CAMUNDA_WORKER_MAX_CONCURRENT_JOBS"); err != nil {
		return wd, err
	} else if v > 0 {
		wd.MaxConcurrentJobs = v
	}
	if v, err := parseMillis(get("CAMUNDA_WORKER_REQUEST_TIMEOUT"), "CAMUNDA_WORKER_REQUEST_TIMEOUT"); err != nil {
		return wd, err
	} else if v > 0 {
		wd.RequestTimeoutMs = v
	}
	if v := get("CAMUNDA_WORKER_NAME"); v != "" {
		wd.Name = v
	}
	if v, err := parseInt(get("CAMUNDA_WORKER_STARTUP_JITTER_MAX_SECONDS"), "CAMUNDA_WORKER_STARTUP_JITTER_MAX_SECONDS"); err != nil {
		return wd, err
	} else if v > 0 {
		wd.StartupJitterMaxSeconds = v
	}
	return wd, nil
}

// isNilUnderlyingValue reports whether v holds a nil pointer, map, slice, channel or
// func. Such a value is not equal to nil as an interface, so it passes a nil check
// while having no usable underlying value. The pointer case is the one that bites --
// it is how nearly every Go type satisfies an interface, and the first method call
// touching a field panics. The others are included because none of them can be a
// working Clock, not because every use of them panics.
func isNilUnderlyingValue(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}

// Validate performs fail-fast validation, returning an actionable error (wrapping
// ErrConfig) when the configuration cannot support the selected strategy.
func (c *Config) Validate() error {
	// A nil pointer inside a non-nil interface passes an `!= nil` check but panics on
	// the first method call, far from WithClock and long after New returned.
	if c.Clock != nil && isNilUnderlyingValue(c.Clock) {
		return configErrorf("WithClock was given a nil %T", c.Clock)
	}
	u, err := url.Parse(c.RestAddress)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return configErrorf("CAMUNDA_REST_ADDRESS %q is not a valid http(s) URL", c.RestAddress)
	}
	switch c.AuthStrategy {
	case AuthOAuth:
		var missing []string
		if c.ClientID == "" {
			missing = append(missing, "CAMUNDA_CLIENT_ID")
		}
		if c.ClientSecret == "" {
			missing = append(missing, "CAMUNDA_CLIENT_SECRET")
		}
		if c.OAuthURL == "" {
			missing = append(missing, "CAMUNDA_OAUTH_URL")
		}
		if len(missing) > 0 {
			return configErrorf("OAuth strategy requires %s", strings.Join(missing, ", "))
		}
		if _, err := url.Parse(c.OAuthURL); err != nil {
			return configErrorf("CAMUNDA_OAUTH_URL %q is not a valid URL", c.OAuthURL)
		}
	case AuthBasic:
		if c.BasicAuthUsername == "" || c.BasicAuthPassword == "" {
			return configErrorf("Basic strategy requires CAMUNDA_BASIC_AUTH_USERNAME and CAMUNDA_BASIC_AUTH_PASSWORD")
		}
	}
	if c.Retry.MaxAttempts < 1 {
		return configErrorf("retry max attempts must be >= 1, got %d", c.Retry.MaxAttempts)
	}
	if c.EventualPollDefault <= 0 {
		return configErrorf("eventual poll default must be > 0")
	}
	return nil
}

// FalconEnabled reports whether the FALCON command-stream transport may be used:
// it must be enabled (CAMUNDA_FALCON) and not force-disabled (CAMUNDA_FORCE_REST).
// It only engages when the gateway actually advertises FALCON support; against
// stock Camunda the SDK stays on REST regardless.
func (c *Config) FalconEnabled() bool { return c.Falcon && !c.ForceREST }

func inferAuthStrategy(c *Config) AuthStrategy {
	switch {
	case c.ClientID != "" && c.ClientSecret != "":
		return AuthOAuth
	case c.BasicAuthUsername != "" && c.BasicAuthPassword != "":
		return AuthBasic
	default:
		return AuthNone
	}
}

// isFalsy reports whether v is an explicit falsy toggle value.
func isFalsy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "off", "false", "no":
		return true
	default:
		return false
	}
}

// isTruthy reports whether v is a non-empty, non-falsy toggle value.
func isTruthy(v string) bool { return strings.TrimSpace(v) != "" && !isFalsy(v) }

func normalizeRestAddress(addr string) string {
	return strings.TrimRight(strings.TrimSpace(addr), "/")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func parseInt(v, key string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, configErrorf("%s must be an integer, got %q", key, v)
	}
	return n, nil
}

func parseMillis(v, key string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, configErrorf("%s must be an integer number of milliseconds, got %q", key, v)
	}
	return n, nil
}
