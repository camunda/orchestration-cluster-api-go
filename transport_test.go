package camunda

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTripperFunc adapts a function to http.RoundTripper for transport tests.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func okResponse(r *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     make(http.Header),
		Request:    r,
	}
}

// TestBuildBaseTransportDefaults pins the defect class of issue #162: the SDK
// must never ride on http.DefaultTransport's 2-idle-conns-per-host pool. Even
// with no TLS material configured, the base transport is the SDK's own clone
// with a workload-sized pool.
func TestBuildBaseTransportDefaults(t *testing.T) {
	cfg, err := loadConfig(noEnv, nil)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	rt, err := buildBaseTransport(cfg)
	if err != nil {
		t.Fatalf("buildBaseTransport: %v", err)
	}
	tr, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("base transport is %T, want *http.Transport (nil would mean http.DefaultTransport)", rt)
	}
	if tr == http.DefaultTransport {
		t.Error("base transport must be the SDK's own clone, not http.DefaultTransport")
	}
	if tr.MaxIdleConns != defaultMaxIdleConns {
		t.Errorf("MaxIdleConns = %d, want %d", tr.MaxIdleConns, defaultMaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != defaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", tr.MaxIdleConnsPerHost, defaultMaxIdleConnsPerHost)
	}
}

func TestBuildBaseTransportPoolSizingFromEnv(t *testing.T) {
	env := map[string]string{
		"CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS":          "64",
		"CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS_PER_HOST": "128",
	}
	cfg, err := loadConfig(envFrom(env), nil)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	rt, err := buildBaseTransport(cfg)
	if err != nil {
		t.Fatalf("buildBaseTransport: %v", err)
	}
	tr := rt.(*http.Transport)
	if tr.MaxIdleConns != 64 || tr.MaxIdleConnsPerHost != 128 {
		t.Errorf("pool = %d/%d, want 64/128", tr.MaxIdleConns, tr.MaxIdleConnsPerHost)
	}
}

func TestBuildBaseTransportPoolSizingFromOptions(t *testing.T) {
	cfg, err := loadConfig(noEnv, nil, WithMaxIdleConns(512), WithMaxIdleConnsPerHost(1024))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	rt, err := buildBaseTransport(cfg)
	if err != nil {
		t.Fatalf("buildBaseTransport: %v", err)
	}
	tr := rt.(*http.Transport)
	if tr.MaxIdleConns != 512 || tr.MaxIdleConnsPerHost != 1024 {
		t.Errorf("pool = %d/%d, want 512/1024", tr.MaxIdleConns, tr.MaxIdleConnsPerHost)
	}
}

func TestBuildBaseTransportInvalidEnv(t *testing.T) {
	env := map[string]string{"CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS_PER_HOST": "lots"}
	if _, err := loadConfig(envFrom(env), nil); err == nil {
		t.Fatal("expected a configuration error for a non-integer pool size")
	} else if !errors.Is(err, ErrConfig) {
		t.Fatalf("expected ErrConfig, got %v", err)
	}
}

func TestBuildBaseTransportUserTransportWins(t *testing.T) {
	called := false
	mine := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return okResponse(r), nil
	})
	cfg, err := loadConfig(noEnv, nil, WithTransport(mine))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	rt, err := buildBaseTransport(cfg)
	if err != nil {
		t.Fatalf("buildBaseTransport: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://localhost:8080/v2/topology", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if !called {
		t.Error("expected the base transport to be the user-supplied RoundTripper")
	}
}

func TestWithHTTPClientUsedAsIs(t *testing.T) {
	mine := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) { return okResponse(r), nil })}
	c, err := New(WithHTTPClient(mine))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Raw().GetConfig().HTTPClient != mine {
		t.Error("expected the generated client to use the supplied *http.Client as-is")
	}
}

func TestWithHTTPClientAndTransportConflict(t *testing.T) {
	_, err := New(
		WithHTTPClient(&http.Client{}),
		WithTransport(roundTripperFunc(func(r *http.Request) (*http.Response, error) { return okResponse(r), nil })),
	)
	if err == nil {
		t.Fatal("expected a configuration error when both WithHTTPClient and WithTransport are set")
	}
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("expected ErrConfig, got %v", err)
	}
}

// TestWithTransportStaysUnderSDKChain pins that a user-supplied RoundTripper
// sits at the bottom of the SDK chain: auth is still applied on top of it.
func TestWithTransportStaysUnderSDKChain(t *testing.T) {
	var sawAuth string
	mine := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		return okResponse(r), nil
	})
	c, err := New(WithTransport(mine), WithBasicAuth("u", "p"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://localhost:8080/v2/topology", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.Raw().GetConfig().HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if sawAuth == "" {
		t.Error("expected the SDK auth layer to set an Authorization header above the user transport")
	}
}

// Explicit env pool values are preserved (not replaced by the default) so that
// negatives are rejected and zero matches the equivalent option.
func TestHTTPPoolEnvExplicitValuesPreserved(t *testing.T) {
	for _, key := range []string{"CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS", "CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS_PER_HOST"} {
		t.Run(key+"=-1", func(t *testing.T) {
			_, err := loadConfig(envFrom(map[string]string{key: "-1"}), nil)
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("expected ErrConfig for negative %s, got %v", key, err)
			}
		})
		t.Run(key+"=0", func(t *testing.T) {
			cfg, err := loadConfig(envFrom(map[string]string{key: "0"}), nil)
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			got := cfg.MaxIdleConns
			if key == "CAMUNDA_SDK_HTTP_MAX_IDLE_CONNS_PER_HOST" {
				got = cfg.MaxIdleConnsPerHost
			}
			if got != 0 {
				t.Fatalf("%s=0 resolved to %d, want 0", key, got)
			}
		})
	}
}

func TestWithTransportTypedNilRejected(t *testing.T) {
	_, err := New(WithTransport((*http.Transport)(nil)))
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("expected ErrConfig for typed-nil transport, got %v", err)
	}
}

// WithHTTPClient bypasses SDK TLS: an unreadable SDK CA path must not fail New.
func TestWithHTTPClientSkipsSDKTransportSetup(t *testing.T) {
	t.Setenv("CAMUNDA_MTLS_CA_PATH", "/nonexistent/ca.pem")
	if _, err := New(WithHTTPClient(&http.Client{})); err != nil {
		t.Fatalf("New with custom HTTP client: %v", err)
	}
}

// FALCON probes/upgrades must go through caller-supplied plumbing.
func TestFalconDialerUsesCustomPlumbing(t *testing.T) {
	mine := &http.Client{}
	c, err := New(WithHTTPClient(mine))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d, err := c.buildFalconDialer()
	if err != nil {
		t.Fatalf("buildFalconDialer: %v", err)
	}
	if d.HTTPClient != mine {
		t.Fatal("falcon dialer did not use the WithHTTPClient client")
	}

	called := false
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return okResponse(r), nil
	})
	c2, err := New(WithTransport(rt))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d2, err := c2.buildFalconDialer()
	if err != nil {
		t.Fatalf("buildFalconDialer: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	resp, err := d2.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if !called {
		t.Fatal("falcon dialer bypassed the WithTransport round tripper")
	}
}
