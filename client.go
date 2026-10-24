package camunda

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
	"github.com/camunda/orchestration-cluster-api-go/v11/internal/auth"
	"github.com/camunda/orchestration-cluster-api-go/v11/internal/backpressure"
	"github.com/camunda/orchestration-cluster-api-go/v11/internal/diag"
	"github.com/camunda/orchestration-cluster-api-go/v11/internal/falcon"
	"github.com/camunda/orchestration-cluster-api-go/v11/internal/retry"
	"github.com/camunda/orchestration-cluster-api-go/v11/internal/transport"
)

// CamundaClient is the ergonomic entry point to the Camunda 8 Orchestration
// Cluster API. It wraps the generated REST client with configuration,
// authentication, adaptive backpressure, and transient retry. Its per-operation
// methods are generated in facade_generated.go.
type CamundaClient struct {
	cfg    *Config
	raw    *camundaapi.APIClient
	logger *diag.Logger
	bp     *backpressure.Manager
	clock  Clock
	random Random

	// FALCON (nanobpmn command-stream) state, lazily resolved and shared for the
	// client's lifetime. falconMu guards a probe that caches a definitive result
	// (nano detected, or confirmed stock) but retries after a transient failure;
	// falconProdOnce builds the shared create producer on first stream create.
	falconMu        sync.Mutex
	falconResolved  bool
	falconProbing   bool
	falconLastProbe time.Time
	falconCapsV     *falcon.Caps
	falconDialer    *falcon.Dialer
	falconProdOnce  sync.Once
	falconProd      *falcon.Producer
	falconProdErr   error
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
	// Resolve the clock before any collaborator, so they all share one timeline.
	clk := cfg.Clock
	if clk == nil {
		clk = LiveClock{}
	}
	rnd := cfg.Random
	if rnd == nil {
		rnd = LiveRandom{}
	}

	authT, err := buildAuthTransport(cfg, clk)
	if err != nil {
		return nil, err
	}
	base, err := buildBaseTransport(cfg.TLS)
	if err != nil {
		return nil, err
	}
	bp := backpressure.New(backpressureProfile(cfg.BackpressureProfile), clk)

	rt := transport.New(transport.Options{
		Base:         base,
		Auth:         authT,
		Retry:        retry.Config{MaxAttempts: cfg.Retry.MaxAttempts, BaseDelay: cfg.Retry.BaseDelay, MaxDelay: cfg.Retry.MaxDelay},
		Backpressure: bp,
		Exempt:       exemptDrainOps,
	}, clk, rnd)

	oc := camundaapi.NewConfiguration()
	oc.HTTPClient = &http.Client{Transport: rt}
	oc.Servers = camundaapi.ServerConfigurations{{URL: v2BaseURL(cfg.RestAddress, cfg.ExactRestAddress)}}
	if err := rebaseOperationServers(oc, cfg); err != nil {
		return nil, err
	}

	return &CamundaClient{
		cfg:    cfg,
		raw:    camundaapi.NewAPIClient(oc),
		logger: diag.New(logLevel(cfg.LogLevel), nil, clk),
		bp:     bp,
		clock:  clk,
		random: rnd,
	}, nil
}

// Clock returns the clock this client resolves cadence through.
func (c *CamundaClient) Clock() Clock { return c.clock }

// Random returns the source this client draws jitter from.
func (c *CamundaClient) Random() Random { return c.random }

// Raw returns the underlying generated client for operations or options not yet
// surfaced on the ergonomic facade.
func (c *CamundaClient) Raw() *camundaapi.APIClient { return c.raw }

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
		var apiErr *camundaapi.GenericOpenAPIError
		if errors.As(err, &apiErr) {
			body = string(apiErr.Body())
		}
		return &APIError{Status: status, Body: body}
	}
	return err
}

func buildAuthTransport(cfg *Config, clk auth.Clock) (*auth.Transport, error) {
	switch cfg.AuthStrategy {
	case AuthOAuth:
		ts := auth.NewTokenSource(auth.OAuthConfig{
			TokenURL:     cfg.OAuthURL,
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Audience:     cfg.TokenAudience,
			Scope:        cfg.OAuthScope,
			CacheDir:     cfg.OAuthCacheDir,
		}, clk)
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

// buildTLSConfig builds a *tls.Config from the configured TLS material, or nil
// when no TLS material is configured. It is shared by the REST base transport
// and the gRPC streaming worker.
func buildTLSConfig(t TLSConfig) (*tls.Config, error) {
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
	return tlsConf, nil
}

// buildBaseTransport returns a TLS-configured base RoundTripper, or nil (meaning
// the default transport) when no TLS material is configured.
func buildBaseTransport(t TLSConfig) (http.RoundTripper, error) {
	tlsConf, err := buildTLSConfig(t)
	if err != nil {
		return nil, err
	}
	if tlsConf == nil {
		return nil, nil
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

// v2BaseURL ensures the REST base address targets the /v2 API root. When exact
// is true the caller has opted out of suffix normalization (see
// Config.ExactRestAddress) and the address is returned verbatim (trimmed of a
// trailing slash) so gateway/reverse-proxy fronted deployments can use a base
// path that does not follow the .../v2 convention.
func v2BaseURL(addr string, exact bool) string {
	addr = strings.TrimRight(addr, "/")
	if exact {
		return addr
	}
	if strings.HasSuffix(addr, "/v2") {
		return addr
	}
	return addr + "/v2"
}

// OperationServerOrigin derives the origin (scheme://host[:port]) that
// operation-level servers — the cluster-admin /cluster/v2/* operations, whose
// spec template is "{schema}://{host}:{port}" with no path — resolve against.
// It is the configured REST address with any base path stripped: the spec paths
// for these operations already carry their own prefix, so appending the REST
// base path (/v2, or a gateway-fronted path under ExactRestAddress) would
// double it. The origin is serialized through url.URL, not concatenated, so an
// IPv6 literal keeps its brackets and a scoped literal keeps its
// percent-escaped zone identifier (url.Parse decodes %25 in u.Host; a plain
// scheme+"://"+host join would emit an invalid URL containing a raw "%eth0").
// A REST address that is not a valid http(s) URL is an error, never a
// best-guess origin.
func OperationServerOrigin(restAddress string, exact bool) (string, error) {
	u, err := url.Parse(v2BaseURL(restAddress, exact))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", configErrorf("CAMUNDA_REST_ADDRESS %q is not a valid http(s) URL", restAddress)
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), nil
}

// rebaseOperationServers rewrites every operation-level server the generator
// registered on cfg (the cluster-admin operations) so it resolves against the
// configured REST address instead of the spec template defaults
// (http://localhost:8080). The URL template itself ("{schema}://{host}:{port}")
// is retained and only the variable *defaults* are rebased onto the configured
// origin, so the context-based override mechanisms — ContextServerVariables and
// ContextOperationServerVariables — keep working: a caller that supplies
// variables via context still replaces the {schema}/{host}/{port} placeholders
// at request time. An entry whose template does not carry the expected
// variables is an error (fail closed), never silently left pointing at the
// spec defaults.
func rebaseOperationServers(oc *camundaapi.Configuration, cfg *Config) error {
	if len(oc.OperationServers) == 0 {
		return nil
	}
	origin, err := OperationServerOrigin(cfg.RestAddress, cfg.ExactRestAddress)
	if err != nil {
		return err
	}
	u, err := url.Parse(origin) // OperationServerOrigin already validated it
	if err != nil {
		return err
	}
	for name, servers := range oc.OperationServers {
		for i := range servers {
			if err := rebaseServerVariables(&servers[i], u); err != nil {
				return fmt.Errorf("operation server %q: %w", name, err)
			}
		}
		oc.OperationServers[name] = servers
	}
	return nil
}

// rebaseServerVariables rewrites the schema/host/port variable defaults of one
// server entry to the components of origin. It fails closed when the entry's
// URL template does not declare all three variables: leaving such an entry
// untouched would silently keep it on the spec defaults (localhost:8080),
// which is exactly the defect this function exists to remove.
func rebaseServerVariables(server *camundaapi.ServerConfiguration, origin *url.URL) error {
	port := origin.Port()
	if port == "" {
		switch origin.Scheme {
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}
	defaults := map[string]string{
		"schema": origin.Scheme,
		"host":   hostVariableDefault(origin),
		"port":   port,
	}
	for name, value := range defaults {
		v, ok := server.Variables[name]
		if !ok || !strings.Contains(server.URL, "{"+name+"}") {
			return configErrorf("URL template %q does not declare a {%s} variable; cannot rebase it onto the configured REST address", server.URL, name)
		}
		v.DefaultValue = value
		server.Variables[name] = v
	}
	return nil
}

// hostVariableDefault renders origin's host as a URL-authority host fragment
// for the {host} variable default. url.URL.Hostname strips IPv6 brackets and
// decodes the zone identifier's %25 escape, so a raw Hostname() default would
// resolve the retained template to an invalid authority ("http://::1:9999")
// or lose the zone's escaping. An IPv6 literal (identified by the colons a
// reg-name can never contain) is re-wrapped in brackets and its zone
// identifier re-escaped; anything else is already a valid host fragment.
func hostVariableDefault(origin *url.URL) string {
	host := origin.Hostname()
	if !strings.Contains(host, ":") {
		return host
	}
	if i := strings.LastIndex(host, "%"); i >= 0 {
		return "[" + host[:i] + "%25" + host[i+1:] + "]"
	}
	return "[" + host + "]"
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
