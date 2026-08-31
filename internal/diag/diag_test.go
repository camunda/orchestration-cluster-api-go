package diag

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"":        LevelInfo,
		"info":    LevelInfo,
		"OFF":     LevelOff,
		"none":    LevelOff,
		"Error":   LevelError,
		"warn":    LevelWarn,
		"warning": LevelWarn,
		"debug":   LevelDebug,
		"trace":   LevelTrace,
		"verbose": LevelTrace,
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil {
			t.Fatalf("ParseLevel(%q) unexpected error: %v", in, err)
		}
		if got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseLevel("bogus"); err == nil {
		t.Error("ParseLevel(bogus) expected error, got nil")
	}
}

func TestLoggerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelWarn, &buf, fixedClock{})
	l.Debug("should-not-appear")
	l.Info("also-not")
	l.Warn("appears", "k", "v")
	l.Error("appears-too")
	out := buf.String()
	if strings.Contains(out, "should-not-appear") || strings.Contains(out, "also-not") {
		t.Errorf("below-threshold records leaked: %q", out)
	}
	if !strings.Contains(out, "appears") || !strings.Contains(out, "k=v") {
		t.Errorf("expected warn record with kv, got %q", out)
	}
	if !strings.Contains(out, "appears-too") {
		t.Errorf("expected error record, got %q", out)
	}
}

func TestLoggerOffSuppressesEverything(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelOff, &buf, fixedClock{})
	l.Error("nope")
	if buf.Len() != 0 {
		t.Errorf("LevelOff should suppress all output, got %q", buf.String())
	}
}

func TestEnvironmentSnapshotRedactsSecrets(t *testing.T) {
	environ := []string{
		"CAMUNDA_REST_ADDRESS=http://localhost:8080",
		"CAMUNDA_CLIENT_SECRET=super-secret",
		"CAMUNDA_CLIENT_ID=my-client",
		"ZEEBE_CLIENT_SECRET=other-secret",
		"CAMUNDA_OAUTH_CACHE_DIR=/tmp/cache",
		"CAMUNDA_MTLS_KEY_PATH=/etc/key.pem",
		"UNRELATED_VAR=ignored",
	}
	snap := EnvironmentSnapshot(environ)
	if _, ok := snap["UNRELATED_VAR"]; ok {
		t.Error("non-CAMUNDA/ZEEBE var should be excluded")
	}
	if snap["CAMUNDA_REST_ADDRESS"] != "http://localhost:8080" {
		t.Errorf("address should be preserved, got %q", snap["CAMUNDA_REST_ADDRESS"])
	}
	if snap["CAMUNDA_CLIENT_SECRET"] != "***redacted***" {
		t.Errorf("secret should be redacted, got %q", snap["CAMUNDA_CLIENT_SECRET"])
	}
	if snap["ZEEBE_CLIENT_SECRET"] != "***redacted***" {
		t.Errorf("zeebe secret should be redacted, got %q", snap["ZEEBE_CLIENT_SECRET"])
	}
	if snap["CAMUNDA_CLIENT_ID"] != "my-client" {
		t.Errorf("client id is not a secret, got %q", snap["CAMUNDA_CLIENT_ID"])
	}
	if snap["CAMUNDA_OAUTH_CACHE_DIR"] != "/tmp/cache" {
		t.Errorf("cache dir is a location not a secret, got %q", snap["CAMUNDA_OAUTH_CACHE_DIR"])
	}
	if snap["CAMUNDA_MTLS_KEY_PATH"] != "/etc/key.pem" {
		t.Errorf("key path is a location not a secret, got %q", snap["CAMUNDA_MTLS_KEY_PATH"])
	}
}

// fixedClock pins log timestamps so output is comparable.
type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(1_000_000, 0).UTC() }
