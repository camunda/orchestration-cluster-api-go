package camunda

import (
	"errors"
	"testing"
	"time"
)

// noEnv is an environment lookup that returns nothing.
func noEnv(string) string { return "" }

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaultsWhenEnvEmpty(t *testing.T) {
	cfg, err := loadConfig(noEnv, nil)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.RestAddress != defaultRestAddress {
		t.Errorf("RestAddress = %q, want default", cfg.RestAddress)
	}
	if cfg.AuthStrategy != AuthNone {
		t.Errorf("AuthStrategy = %v, want AuthNone", cfg.AuthStrategy)
	}
	if cfg.BackpressureProfile != ProfileBalanced {
		t.Errorf("BackpressureProfile = %v, want Balanced", cfg.BackpressureProfile)
	}
	if cfg.Retry.MaxAttempts != defaultRetryMaxAttempts {
		t.Errorf("Retry.MaxAttempts = %d, want %d", cfg.Retry.MaxAttempts, defaultRetryMaxAttempts)
	}
	if cfg.EventualPollDefault != defaultEventualPollDefault {
		t.Errorf("EventualPollDefault = %v, want %v", cfg.EventualPollDefault, defaultEventualPollDefault)
	}
	if cfg.WorkerDefaults.Name != defaultWorkerName {
		t.Errorf("WorkerDefaults.Name = %q, want %q", cfg.WorkerDefaults.Name, defaultWorkerName)
	}
}

func TestEnvResolutionAndInference(t *testing.T) {
	env := map[string]string{
		"CAMUNDA_REST_ADDRESS":  "https://cluster.example/v2/",
		"CAMUNDA_CLIENT_ID":     "my-id",
		"CAMUNDA_CLIENT_SECRET": "my-secret",
		"CAMUNDA_OAUTH_URL":     "https://login.example/token",
	}
	cfg, err := loadConfig(envFrom(env), nil)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.RestAddress != "https://cluster.example/v2" {
		t.Errorf("RestAddress trailing slash not normalized: %q", cfg.RestAddress)
	}
	if cfg.AuthStrategy != AuthOAuth {
		t.Errorf("expected OAuth inferred from credentials, got %v", cfg.AuthStrategy)
	}
}

func TestZeebeFallbackKeys(t *testing.T) {
	env := map[string]string{
		"ZEEBE_REST_ADDRESS":             "https://z.example",
		"ZEEBE_CLIENT_ID":                "zid",
		"ZEEBE_CLIENT_SECRET":            "zsec",
		"ZEEBE_AUTHORIZATION_SERVER_URL": "https://z.example/token",
	}
	cfg, err := loadConfig(envFrom(env), nil)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.RestAddress != "https://z.example" || cfg.ClientID != "zid" || cfg.OAuthURL != "https://z.example/token" {
		t.Errorf("ZEEBE_* fallback keys not honored: %+v", cfg)
	}
	if cfg.AuthStrategy != AuthOAuth {
		t.Errorf("expected OAuth inferred, got %v", cfg.AuthStrategy)
	}
}

func TestOptionsOverrideEnv(t *testing.T) {
	env := map[string]string{"CAMUNDA_REST_ADDRESS": "http://from-env:8080"}
	cfg, err := loadConfig(envFrom(env), nil,
		WithRestAddress("http://from-option:9090"),
		WithBackpressureProfile(ProfileLegacy),
	)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.RestAddress != "http://from-option:9090" {
		t.Errorf("option should override env, got %q", cfg.RestAddress)
	}
	if cfg.BackpressureProfile != ProfileLegacy {
		t.Errorf("expected Legacy profile from option, got %v", cfg.BackpressureProfile)
	}
}

func TestValidateOAuthMissingCredentials(t *testing.T) {
	env := map[string]string{"CAMUNDA_AUTH_STRATEGY": "OAUTH"}
	_, err := loadConfig(envFrom(env), nil)
	if err == nil {
		t.Fatal("expected validation error for OAuth without credentials")
	}
	if !errors.Is(err, ErrConfig) {
		t.Errorf("error should wrap ErrConfig, got %v", err)
	}
}

func TestValidateBasicMissingCredentials(t *testing.T) {
	_, err := loadConfig(noEnv, nil, func(c *Config) { c.AuthStrategy = AuthBasic })
	if !errors.Is(err, ErrConfig) {
		t.Errorf("expected ErrConfig for Basic without credentials, got %v", err)
	}
}

func TestValidateBadRestAddress(t *testing.T) {
	_, err := loadConfig(noEnv, nil, WithRestAddress("not a url"))
	if !errors.Is(err, ErrConfig) {
		t.Errorf("expected ErrConfig for bad address, got %v", err)
	}
}

func TestInvalidIntEnvIsActionable(t *testing.T) {
	env := map[string]string{"CAMUNDA_SDK_HTTP_RETRY_MAX_ATTEMPTS": "lots"}
	_, err := loadConfig(envFrom(env), nil)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("expected ErrConfig for non-integer retry attempts, got %v", err)
	}
}

func TestMillisEnvParsed(t *testing.T) {
	env := map[string]string{"CAMUNDA_SDK_EVENTUAL_POLL_DEFAULT_MS": "1500"}
	cfg, err := loadConfig(envFrom(env), nil, WithBasicAuth("u", "p"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.EventualPollDefault != 1500*time.Millisecond {
		t.Errorf("EventualPollDefault = %v, want 1.5s", cfg.EventualPollDefault)
	}
}
