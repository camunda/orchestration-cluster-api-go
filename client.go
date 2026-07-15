package camunda

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"strings"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/internal/auth"
	"github.com/camunda/orchestration-cluster-api-go/internal/backpressure"
	"github.com/camunda/orchestration-cluster-api-go/internal/diag"
	"github.com/camunda/orchestration-cluster-api-go/internal/retry"
	"github.com/camunda/orchestration-cluster-api-go/internal/transport"
)

// CamundaClient is the ergonomic entry point to the Camunda 8 Orchestration
// Cluster API. It wraps the generated REST client with configuration,
// authentication, adaptive backpressure, and transient retry. Its per-operation
// methods are generated in facade_generated.go.
type CamundaClient struct {
	cfg    *Config
	raw    *openapi.APIClient
	logger *diag.Logger
	bp     *backpressure.Manager
}

// New resolves configuration from environment variables and options, then builds
// a ready-to-use client. Options take precedence over the environment.
func New(opts ...Option) (*CamundaClient, error) {
	cfg, err := LoadConfig(opts...)
	if err != nil {
		return nil, err
	}
	return newFromConfig(cfg)
}

func newFromConfig(cfg *Config) (*CamundaClient, error) {
	authT, err := buildAuthTransport(cfg)
	if err != nil {
		return nil, err
	}
	base, err := buildBaseTransport(cfg.TLS)
	if err != nil {
		return nil, err
	}
	bp := backpressure.New(backpressureProfile(cfg.BackpressureProfile))

	rt := transport.New(transport.Options{
		Base:         base,
		Auth:         authT,
		Retry:        retry.Config{MaxAttempts: cfg.Retry.MaxAttempts, BaseDelay: cfg.Retry.BaseDelay, MaxDelay: cfg.Retry.MaxDelay},
		Backpressure: bp,
		Exempt:       exemptDrainOps,
	})

	oc := openapi.NewConfiguration()
	oc.HTTPClient = &http.Client{Transport: rt}
	oc.Servers = openapi.ServerConfigurations{{URL: v2BaseURL(cfg.RestAddress)}}

	return &CamundaClient{
		cfg:    cfg,
		raw:    openapi.NewAPIClient(oc),
		logger: diag.New(logLevel(cfg.LogLevel), nil),
		bp:     bp,
	}, nil
}

// Raw returns the underlying generated client for operations or options not yet
// surfaced on the ergonomic facade.
func (c *CamundaClient) Raw() *openapi.APIClient { return c.raw }

// Config returns the resolved configuration.
func (c *CamundaClient) Config() *Config { return c.cfg }

// wrapError maps a generated-client error and its HTTP response into the SDK's
// error taxonomy: an *APIError for non-success HTTP responses, otherwise the
// underlying transport error.
func (c *CamundaClient) wrapError(resp *http.Response, err error) error {
	if err == nil {
		return nil
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	if status >= 400 {
		body := ""
		var apiErr *openapi.GenericOpenAPIError
		if errors.As(err, &apiErr) {
			body = string(apiErr.Body())
		}
		return &APIError{Status: status, Body: body}
	}
	return err
}

func buildAuthTransport(cfg *Config) (*auth.Transport, error) {
	switch cfg.AuthStrategy {
	case AuthOAuth:
		ts := auth.NewTokenSource(auth.OAuthConfig{
			TokenURL:     cfg.OAuthURL,
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Audience:     cfg.TokenAudience,
			Scope:        cfg.OAuthScope,
			CacheDir:     cfg.OAuthCacheDir,
		})
		return &auth.Transport{Strategy: auth.OAuth, TokenSource: ts}, nil
	case AuthBasic:
		return &auth.Transport{
			Strategy:      auth.Basic,
			BasicUsername: cfg.BasicAuthUsername,
			BasicPassword: cfg.BasicAuthPassword,
		}, nil
	default:
		return &auth.Transport{Strategy: auth.None}, nil
	}
}

// buildBaseTransport returns a TLS-configured base RoundTripper, or nil (meaning
// the default transport) when no TLS material is configured.
func buildBaseTransport(t TLSConfig) (http.RoundTripper, error) {
	if !t.IsConfigured() {
		return nil, nil
	}
	if t.KeyPassphrase != "" {
		return nil, configErrorf("encrypted mTLS keys (CAMUNDA_MTLS_KEY_PASSPHRASE) are not supported yet")
	}
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}

	certPEM, err := pemBytes(t.Cert, t.CertPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := pemBytes(t.Key, t.KeyPath)
	if err != nil {
		return nil, err
	}
	if len(certPEM) > 0 && len(keyPEM) > 0 {
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, configErrorf("invalid mTLS client certificate/key: %v", err)
		}
		tlsConf.Certificates = []tls.Certificate{pair}
	}

	caPEM, err := pemBytes(t.CA, t.CAPath)
	if err != nil {
		return nil, err
	}
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, configErrorf("invalid mTLS CA certificate")
		}
		tlsConf.RootCAs = pool
	}

	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, configErrorf("unexpected default transport type")
	}
	clone := tr.Clone()
	clone.TLSClientConfig = tlsConf
	return clone, nil
}

// pemBytes returns inline PEM if set, else the contents of path, else nil.
func pemBytes(inline, path string) ([]byte, error) {
	if inline != "" {
		return []byte(inline), nil
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, configErrorf("reading TLS material %q: %v", path, err)
		}
		return b, nil
	}
	return nil, nil
}

// exemptDrainOps reports whether a request is a drain operation (job
// completion/failure/error, user-task completion) that should bypass the
// backpressure gate.
func exemptDrainOps(req *http.Request) bool {
	p := req.URL.Path
	if strings.Contains(p, "/jobs/") &&
		(strings.HasSuffix(p, "/completion") || strings.HasSuffix(p, "/failure") || strings.HasSuffix(p, "/error")) {
		return true
	}
	if strings.Contains(p, "/user-tasks/") && strings.HasSuffix(p, "/completion") {
		return true
	}
	return false
}

// v2BaseURL ensures the REST base address targets the /v2 API root.
func v2BaseURL(addr string) string {
	addr = strings.TrimRight(addr, "/")
	if strings.HasSuffix(addr, "/v2") {
		return addr
	}
	return addr + "/v2"
}

func backpressureProfile(p BackpressureProfile) backpressure.Profile {
	if p == ProfileLegacy {
		return backpressure.Legacy
	}
	return backpressure.Balanced
}

func logLevel(l LogLevel) diag.Level {
	switch l {
	case LogOff:
		return diag.LevelOff
	case LogError:
		return diag.LevelError
	case LogWarn:
		return diag.LevelWarn
	case LogDebug:
		return diag.LevelDebug
	case LogTrace:
		return diag.LevelTrace
	default:
		return diag.LevelInfo
	}
}
