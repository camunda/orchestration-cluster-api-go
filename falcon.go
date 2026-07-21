package camunda

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
	"github.com/camunda/orchestration-cluster-api-go/internal/auth"
	"github.com/camunda/orchestration-cluster-api-go/internal/falcon"
)

// falconDetectTimeout bounds the one-time topology probe.
const falconDetectTimeout = 10 * time.Second

// falconCaps probes the gateway once for nanobpmn command-stream support,
// returning nil for stock Camunda, when FALCON is disabled, or when the probe
// fails. The result (and the dialer) are cached for the client's lifetime.
func (c *CamundaClient) falconCaps(ctx context.Context) *falcon.Caps {
	if !c.cfg.FalconEnabled() {
		return nil
	}
	c.falconOnce.Do(func() {
		d, err := c.buildFalconDialer()
		if err != nil {
			c.logger.Warn("falcon dialer unavailable; using REST", "error", err)
			return
		}
		c.falconDialer = d
		pctx, cancel := context.WithTimeout(ctx, falconDetectTimeout)
		defer cancel()
		if caps, ok := falcon.Detect(pctx, v2BaseURL(c.cfg.RestAddress), d.HTTPClient); ok {
			c.falconCapsV = caps
			c.logger.Debug("falcon command stream detected", "endpoints", caps.Endpoints)
		}
	})
	return c.falconCapsV
}

// buildFalconDialer constructs a WebSocket dialer whose HTTP client carries the
// SDK's TLS material (for wss://) and injects the configured authentication on
// both the topology probe and the WebSocket upgrade handshake.
func (c *CamundaClient) buildFalconDialer() (*falcon.Dialer, error) {
	tlsConf, err := buildTLSConfig(c.cfg.TLS)
	if err != nil {
		return nil, err
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
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
			for _, v := range vs {
				r.Header.Set(k, v)
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

	result := &openapi.CreateProcessInstanceResult{
		ProcessDefinitionId:  id,
		ProcessInstanceKey:   openapi.ModelString(outcome.ProcessInstanceKey),
		ProcessDefinitionKey: openapi.ModelString(key),
		Variables:            outcome.Variables,
		Tags:                 []string{},
		TenantId:             c.resolveTenant(tenant),
		BusinessId:           *openapi.NewNullableString(nil),
	}
	if version != nil {
		result.ProcessDefinitionVersion = *version
	}
	if result.Variables == nil {
		result.Variables = map[string]interface{}{}
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
