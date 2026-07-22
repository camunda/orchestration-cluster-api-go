package camunda

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/camunda/orchestration-cluster-api-go/internal/auth"
)

func TestConfigurationOptionsApplyAllValues(t *testing.T) {
	retryCfg := RetryConfig{MaxAttempts: 7, BaseDelay: 2 * time.Second, MaxDelay: 9 * time.Second}
	cacheDir := t.TempDir()
	cfg, err := loadConfig(noEnv, nil,
		WithRestAddress("http://cluster.example/"),
		WithGrpcAddress("cluster.example:26500"),
		WithOAuth("client", "secret", "https://login.example/token"),
		WithOAuthAudience("audience"),
		WithOAuthScope("scope"),
		WithOAuthCacheDir(cacheDir),
		WithBackpressureProfile(ProfileLegacy),
		WithLogLevel(LogTrace),
		WithRetry(retryCfg),
		WithDefaultTenantID("tenant-a"),
	)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.RestAddress != "http://cluster.example" ||
		cfg.GrpcAddress != "cluster.example:26500" ||
		cfg.AuthStrategy != AuthOAuth ||
		cfg.ClientID != "client" ||
		cfg.ClientSecret != "secret" ||
		cfg.OAuthURL != "https://login.example/token" ||
		cfg.TokenAudience != "audience" ||
		cfg.OAuthScope != "scope" ||
		cfg.OAuthCacheDir != cacheDir ||
		cfg.BackpressureProfile != ProfileLegacy ||
		cfg.LogLevel != LogTrace ||
		cfg.DefaultTenantID != "tenant-a" {
		t.Fatalf("options were not fully applied: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.Retry, retryCfg) {
		t.Errorf("Retry = %+v, want %+v", cfg.Retry, retryCfg)
	}
}

func TestWorkerOptionsApplyRequestControls(t *testing.T) {
	client, err := New(WithNoAuth())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	worker := client.NewJobWorker("work", nil,
		WithWorkerName("worker-a"),
		WithMaxConcurrentJobs(3),
		WithJobTimeout(12*time.Second),
		WithRequestTimeout(4*time.Second),
		WithPollInterval(50*time.Millisecond),
		WithFetchVariables("orderId", "amount"),
		WithWorkerTenantIDs("tenant-a", "tenant-b"),
	)
	if worker.name != "worker-a" ||
		worker.maxConcurrent != 3 ||
		worker.timeout != 12*time.Second ||
		worker.requestTimeout != 4*time.Second ||
		worker.pollInterval != 50*time.Millisecond ||
		!reflect.DeepEqual(worker.fetchVariables, []string{"orderId", "amount"}) ||
		!reflect.DeepEqual(worker.tenantIDs, []string{"tenant-a", "tenant-b"}) {
		t.Fatalf("worker options were not fully applied: %+v", worker)
	}

	stream := client.NewStreamJobWorker("work", nil,
		WithStreamWorkerName("stream-a"),
		WithStreamMaxConcurrentJobs(5),
		WithStreamJobTimeout(20*time.Second),
		WithStreamFetchVariables("customerId"),
		WithStreamReconnectBackoff(75*time.Millisecond),
		WithStreamPollInterval(10*time.Second),
		WithStreamPollMaxJobs(17),
		WithStreamTenantIDs("tenant-c"),
	)
	if stream.name != "stream-a" ||
		stream.maxConcurrent != 5 ||
		stream.timeout != 20*time.Second ||
		stream.reconnectBackoff != 75*time.Millisecond ||
		stream.pollInterval != 10*time.Second ||
		stream.pollMaxJobs != 17 ||
		!reflect.DeepEqual(stream.fetchVariables, []string{"customerId"}) ||
		!reflect.DeepEqual(stream.tenantIDs, []string{"tenant-c"}) {
		t.Fatalf("stream worker options were not fully applied: %+v", stream)
	}
}

func TestBuildTLSConfigLoadsCAFromInlineAndFile(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

	for _, tc := range []struct {
		name string
		cfg  TLSConfig
	}{
		{name: "inline", cfg: TLSConfig{CA: string(certPEM)}},
		{name: "file", cfg: TLSConfig{CAPath: writeTLSFile(t, "ca.pem", certPEM)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tlsCfg, err := buildTLSConfig(tc.cfg)
			if err != nil {
				t.Fatalf("buildTLSConfig: %v", err)
			}
			if tlsCfg.RootCAs == nil {
				t.Fatal("expected configured root CA pool")
			}
			if tlsCfg.MinVersion == 0 {
				t.Fatal("expected an explicit minimum TLS version")
			}
		})
	}
}

func TestBuildTLSConfigRejectsInvalidMaterial(t *testing.T) {
	tests := []struct {
		name string
		cfg  TLSConfig
	}{
		{name: "passphrase", cfg: TLSConfig{CA: "x", KeyPassphrase: "secret"}},
		{name: "invalid client pair", cfg: TLSConfig{Cert: "not a cert", Key: "not a key"}},
		{name: "invalid CA", cfg: TLSConfig{CA: "not a cert"}},
		{name: "unreadable file", cfg: TLSConfig{CAPath: filepath.Join(t.TempDir(), "missing.pem")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildTLSConfig(tc.cfg); !errors.Is(err, ErrConfig) {
				t.Fatalf("buildTLSConfig error = %v, want ErrConfig", err)
			}
		})
	}
}

func TestPemBytesPrefersInlineMaterial(t *testing.T) {
	got, err := pemBytes("inline", filepath.Join(t.TempDir(), "missing.pem"))
	if err != nil {
		t.Fatalf("pemBytes: %v", err)
	}
	if string(got) != "inline" {
		t.Errorf("pemBytes = %q, want inline material", got)
	}
}

func TestOAuthPerRPCMetadata(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"grpc-token","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	creds := &oauthPerRPC{
		ts: auth.NewTokenSource(auth.OAuthConfig{
			TokenURL:     tokenServer.URL,
			ClientID:     "client",
			ClientSecret: "secret",
		}),
		secure: true,
	}
	md, err := creds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("GetRequestMetadata: %v", err)
	}
	if md["authorization"] != "Bearer grpc-token" {
		t.Errorf("authorization = %q, want bearer token", md["authorization"])
	}
	if !creds.RequireTransportSecurity() {
		t.Error("OAuth credentials should require the configured secure transport")
	}
}

func writeTLSFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write TLS file: %v", err)
	}
	return path
}
