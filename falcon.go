package camunda

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/internal/auth"
	"github.com/camunda/orchestration-cluster-api-go/internal/falcon"
)

// falconDetectTimeout bounds a single topology probe.
const falconDetectTimeout = 10 * time.Second

// falconProbeRetryInterval bounds how often the topology probe is retried after a
// transient failure (unreachable gateway / non-2xx), so a persistently
// unreachable gateway is not re-probed on every request.
const falconProbeRetryInterval = 30 * time.Second

// falconCaps probes the gateway for nanobpmn command-stream support, returning nil
// for stock Camunda or when FALCON is disabled. A definitive result (nano detected
// or confirmed stock) is cached for the client's lifetime; a transient probe
// failure is retried on a later call (after falconProbeRetryInterval). The probe
// honours the caller's context (bounded by falconDetectTimeout), so a short request
// deadline can't make it exceed the caller's budget; because a ctx-cancelled probe
// is treated as transient, a brief first deadline never permanently forces REST.
func (c *CamundaClient) falconCaps(ctx context.Context) *falcon.Caps {
	if !c.cfg.FalconEnabled() {
		return nil
	}
	c.falconMu.Lock()
	if c.falconResolved {
		caps := c.falconCapsV
		c.falconMu.Unlock()
		return caps
	}
	// Another goroutine is already probing, or we're inside the retry backoff after
	// a transient failure: fall back to REST immediately rather than blocking.
	if c.falconProbing ||
		(!c.falconLastProbe.IsZero() && time.Since(c.falconLastProbe) < falconProbeRetryInterval) {
		c.falconMu.Unlock()
		return nil
	}
	if c.falconDialer == nil {
		d, err := c.buildFalconDialer()
		if err != nil {
			c.logger.Warn("falcon dialer unavailable; using REST", "error", err)
			c.falconResolved = true // dialer construction failure is definitive
			c.falconMu.Unlock()
			return nil
		}
		c.falconDialer = d
	}
	c.falconProbing = true
	c.falconLastProbe = time.Now()
	dialer := c.falconDialer
	c.falconMu.Unlock()

	// Probe WITHOUT holding the mutex, so concurrent callers fall back to REST
	// immediately instead of serializing behind a network round-trip. Honour the
	// caller's context but cap it at falconDetectTimeout; a ctx-cancelled probe is
	// transient and retried on a later call.
	pctx, cancel := context.WithTimeout(ctx, falconDetectTimeout)
	caps, err := falcon.Detect(pctx, v2BaseURL(c.cfg.RestAddress), dialer.HTTPClient)
	cancel()

	c.falconMu.Lock()
	c.falconProbing = false
	if err != nil {
		c.falconMu.Unlock()
		c.logger.Debug("falcon detection failed; will retry", "error", err)
		return nil // transient: retry after falconProbeRetryInterval
	}
	c.falconResolved = true // definitive: nano detected or confirmed stock
	c.falconCapsV = caps
	c.falconMu.Unlock()
	if caps != nil {
		c.logger.Debug("falcon command stream detected", "endpoints", caps.Endpoints)
	}
	return caps
}

// buildFalconDialer constructs a WebSocket dialer whose HTTP client carries the
// SDK's TLS material (for wss://) and injects the configured authentication on
// both the topology probe and the WebSocket upgrade handshake.
func (c *CamundaClient) buildFalconDialer() (*falcon.Dialer, error) {
	tlsConf, err := buildTLSConfig(c.cfg.TLS)
	if err != nil {
		return nil, err
	}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("camunda: cannot build falcon dialer: http.DefaultTransport is not *http.Transport")
	}
	base := tr.Clone()
	if tlsConf != nil {
		base.TLSClientConfig = tlsConf
	}

	header, err := c.falconAuthHeader()
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Transport: &falconAuthTransport{base: base, header: header}}
	return &falcon.Dialer{HTTPClient: httpClient}, nil
}

// falconAuthHeader returns a per-request header provider for the configured auth
// strategy, or nil when no auth is configured.
func (c *CamundaClient) falconAuthHeader() (func(context.Context) (http.Header, error), error) {
	switch c.cfg.AuthStrategy {
	case AuthOAuth:
		ts := auth.NewTokenSource(auth.OAuthConfig{
			TokenURL:     c.cfg.OAuthURL,
			ClientID:     c.cfg.ClientID,
			ClientSecret: c.cfg.ClientSecret,
			Audience:     c.cfg.TokenAudience,
			Scope:        c.cfg.OAuthScope,
			CacheDir:     c.cfg.OAuthCacheDir,
		})
		return func(ctx context.Context) (http.Header, error) {
			tok, err := ts.Token(ctx)
			if err != nil {
				return nil, err
			}
			return http.Header{"Authorization": {"Bearer " + tok}}, nil
		}, nil
	case AuthBasic:
		cred := base64.StdEncoding.EncodeToString([]byte(c.cfg.BasicAuthUsername + ":" + c.cfg.BasicAuthPassword))
		return func(context.Context) (http.Header, error) {
			return http.Header{"Authorization": {"Basic " + cred}}, nil
		}, nil
	default:
		return nil, nil
	}
}

// falconAuthTransport injects an Authorization header (from header) onto every
// request before delegating to base. It returns base's response unchanged so the
// WebSocket library can hijack the underlying connection on a 101 upgrade.
type falconAuthTransport struct {
	base   http.RoundTripper
	header func(context.Context) (http.Header, error)
}

func (t *falconAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.header != nil {
		h, err := t.header(r.Context())
		if err != nil {
			return nil, err
		}
		r = r.Clone(r.Context())
		for k, vs := range h {
			r.Header.Del(k)
			for _, v := range vs {
				r.Header.Add(k, v)
			}
		}
	}
	return t.base.RoundTrip(r)
}

// falconProducer returns the shared, lazily-built create producer (one persistent,
// failover-capable link per client).
func (c *CamundaClient) falconProducer(caps *falcon.Caps) (*falcon.Producer, error) {
	c.falconProdOnce.Do(func() {
		c.falconProd, c.falconProdErr = falcon.StartProducer(caps.Endpoints, c.falconDialer)
	})
	return c.falconProd, c.falconProdErr
}

// CreateProcessInstance creates (starts) a process instance.
//
// When the gateway advertises the FALCON command stream (a nanobpmn gateway) and
// FALCON is enabled, the create is routed over the credit-metered WebSocket
// command stream: a flood of creates queues on the client's submission-credit
// window instead of being shed with 503s. Against stock Camunda — or if the
// stream cannot be established — it falls back transparently to the REST endpoint.
//
// The variadic request-builder options apply only on the REST path.
//
// Example:
//
//	instruction := openapi.ProcessInstanceCreationInstructionByIdAsProcessInstanceCreationInstruction(
//		openapi.NewProcessInstanceCreationInstructionById("order-process"))
//	result, err := client.CreateProcessInstance(ctx, instruction)
func (c *CamundaClient) CreateProcessInstance(ctx context.Context, body openapi.ProcessInstanceCreationInstruction, opts ...func(openapi.ApiCreateProcessInstanceRequest) openapi.ApiCreateProcessInstanceRequest) (*openapi.CreateProcessInstanceResult, error) {
	if c.falconCaps(ctx) != nil {
		result, ok, err := c.createProcessInstanceFalcon(ctx, body)
		if err != nil {
			return nil, err
		}
		if ok {
			return result, nil
		}
	}

	req := c.raw.ProcessInstanceAPI.CreateProcessInstance(ctx)
	req = req.ProcessInstanceCreationInstruction(body)
	for _, opt := range opts {
		req = opt(req)
	}
	value, resp, err := req.Execute()
	return value, c.wrapError(resp, err)
}

// createProcessInstanceFalcon routes a create over the command stream. It returns
// (result, true, nil) on success, (nil, false, nil) to signal a transparent
// fall-back to REST (the producer could not be started), or (nil, false, err) for
// a definitive failure (e.g. a non-2xx command result, mapped to *APIError).
func (c *CamundaClient) createProcessInstanceFalcon(ctx context.Context, body openapi.ProcessInstanceCreationInstruction) (*openapi.CreateProcessInstanceResult, bool, error) {
	var (
		id      string
		key     string
		version *int32
		tenant  *string
		args    falcon.CreateArgs
	)
	switch {
	case body.ProcessInstanceCreationInstructionById != nil:
		b := body.ProcessInstanceCreationInstructionById
		id = b.ProcessDefinitionId
		version = b.ProcessDefinitionVersion
		tenant = b.TenantId
		args = falcon.CreateArgs{
			ProcessDefinitionID: b.ProcessDefinitionId,
			Variables:           b.Variables,
			AwaitCompletion:     b.AwaitCompletion != nil && *b.AwaitCompletion,
			FetchVariables:      b.FetchVariables,
		}
		if b.RequestTimeout != nil {
			args.RequestTimeoutMs = *b.RequestTimeout
		}
	case body.ProcessInstanceCreationInstructionByKey != nil:
		b := body.ProcessInstanceCreationInstructionByKey
		key = string(b.ProcessDefinitionKey)
		version = b.ProcessDefinitionVersion
		tenant = b.TenantId
		args = falcon.CreateArgs{
			ProcessDefinitionKey: key,
			Variables:            b.Variables,
			AwaitCompletion:      b.AwaitCompletion != nil && *b.AwaitCompletion,
			FetchVariables:       b.FetchVariables,
		}
		if b.RequestTimeout != nil {
			args.RequestTimeoutMs = *b.RequestTimeout
		}
	default:
		// Unknown union shape: let REST handle (and report) it.
		return nil, false, nil
	}

	caps := c.falconCapsV
	prod, err := c.falconProducer(caps)
	if err != nil {
		c.logger.Warn("falcon producer unavailable; falling back to REST", "error", err)
		return nil, false, nil
	}

	outcome, err := prod.Create(ctx, args)
	if err != nil {
		var re *falcon.RemoteError
		if errors.As(err, &re) {
			return nil, false, &APIError{Status: re.Status, Body: re.Body}
		}
		return nil, false, err
	}

	// Build a REST-equivalent result by overlaying whatever the gateway's
	// commandResult body carries over request-derived defaults.
	var rb struct {
		ProcessDefinitionID      *string        `json:"processDefinitionId"`
		ProcessDefinitionKey     *string        `json:"processDefinitionKey"`
		ProcessDefinitionVersion *int32         `json:"processDefinitionVersion"`
		TenantID                 *string        `json:"tenantId"`
		Variables                map[string]any `json:"variables"`
		Tags                     []string       `json:"tags"`
		BusinessID               *string        `json:"businessId"`
	}
	_ = json.Unmarshal(outcome.Body, &rb)

	result := &openapi.CreateProcessInstanceResult{
		ProcessInstanceKey:   openapi.ModelString(outcome.ProcessInstanceKey),
		ProcessDefinitionId:  id,
		ProcessDefinitionKey: openapi.ModelString(key),
		Tags:                 []string{},
		BusinessId:           *openapi.NewNullableString(nil),
	}
	if rb.ProcessDefinitionID != nil && *rb.ProcessDefinitionID != "" {
		result.ProcessDefinitionId = *rb.ProcessDefinitionID
	}
	if rb.ProcessDefinitionKey != nil && *rb.ProcessDefinitionKey != "" {
		result.ProcessDefinitionKey = openapi.ModelString(*rb.ProcessDefinitionKey)
	}
	switch {
	case rb.ProcessDefinitionVersion != nil:
		result.ProcessDefinitionVersion = *rb.ProcessDefinitionVersion
	case version != nil:
		result.ProcessDefinitionVersion = *version
	}
	switch {
	case rb.TenantID != nil && *rb.TenantID != "":
		result.TenantId = *rb.TenantID
	default:
		result.TenantId = c.resolveTenant(tenant)
	}
	switch {
	case outcome.Variables != nil: // awaitCompletion output variables
		result.Variables = outcome.Variables
	case rb.Variables != nil:
		result.Variables = rb.Variables
	default:
		result.Variables = map[string]interface{}{}
	}
	if rb.Tags != nil {
		result.Tags = rb.Tags
	}
	if rb.BusinessID != nil {
		result.BusinessId = *openapi.NewNullableString(rb.BusinessID)
	}
	return result, true, nil
}

// resolveTenant applies the configured default tenant when the instruction did not
// specify one, matching the REST path's server-side default.
func (c *CamundaClient) resolveTenant(provided *string) string {
	if provided != nil && *provided != "" {
		return *provided
	}
	if c.cfg.DefaultTenantID != "" {
		return c.cfg.DefaultTenantID
	}
	return "<default>"
}
