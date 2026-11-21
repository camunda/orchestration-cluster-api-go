package camunda

import (
	camundaapi "github.com/camunda/orchestration-cluster-api-go/v10/client"
)

// DefaultTenantIdentifier is the tenant id the gateway uses when the caller
// does not address a specific tenant. It is the effective default tenant
// whenever no default tenant is configured (WithDefaultTenantID /
// CAMUNDA_DEFAULT_TENANT_ID), matching the Camunda Java client.
const DefaultTenantIdentifier = "<default>"

// effectiveDefaultTenant returns the configured default tenant id, or
// DefaultTenantIdentifier when none is configured: a zero-config client must
// address the default tenant explicitly so tenant-scoped commands are accepted
// on multi-tenant clusters (and behave identically on single-tenant ones),
// matching the Camunda Java client.
func (c *CamundaClient) effectiveDefaultTenant() string {
	if d := c.cfg.DefaultTenantID; d != "" {
		return d
	}
	return DefaultTenantIdentifier
}

// tenantIDOrDefault converts an optional branded TenantId into the plain *string
// the falcon producer and resolveTenant operate on, applying the effective
// default tenant (configured, else DefaultTenantIdentifier) when the
// instruction did not specify one, so FALCON and REST send the same tenant.
func (c *CamundaClient) tenantIDOrDefault(t *camundaapi.TenantId) *string {
	if t != nil {
		s := string(*t)
		return &s
	}
	d := c.effectiveDefaultTenant()
	return &d
}

// The withDefaultTenant* helpers implement the default-tenant rule for
// operations whose tenant id is an optional field of the request body (or, for
// CreateDeployment, an optional multipart form parameter): when the caller
// omitted the tenant, the effective default tenant — the configured
// WithDefaultTenantID / CAMUNDA_DEFAULT_TENANT_ID, or "<default>" when nothing
// is configured — is injected so the request matches the other Camunda SDKs.
// An explicitly set tenant always wins. The generated facade calls these for
// the tenant-accepting operations; see defaultTenantBodyOps in cmd/facadegen.

func (c *CamundaClient) withDefaultTenantMessagePublication(body MessagePublicationRequest) MessagePublicationRequest {
	if body.TenantId == nil {
		body.SetTenantId(camundaapi.MustTenantId(c.effectiveDefaultTenant()))
	}
	return body
}

func (c *CamundaClient) withDefaultTenantMessageCorrelation(body MessageCorrelationRequest) MessageCorrelationRequest {
	if body.TenantId == nil {
		body.SetTenantId(camundaapi.MustTenantId(c.effectiveDefaultTenant()))
	}
	return body
}

func (c *CamundaClient) withDefaultTenantSignalBroadcast(body SignalBroadcastRequest) SignalBroadcastRequest {
	if body.TenantId == nil {
		body.SetTenantId(camundaapi.MustTenantId(c.effectiveDefaultTenant()))
	}
	return body
}

func (c *CamundaClient) withDefaultTenantConditionalEvaluation(body ConditionalEvaluationInstruction) ConditionalEvaluationInstruction {
	if body.TenantId == nil {
		body.SetTenantId(camundaapi.MustTenantId(c.effectiveDefaultTenant()))
	}
	return body
}

func (c *CamundaClient) withDefaultTenantExpressionEvaluation(body ExpressionEvaluationRequest) ExpressionEvaluationRequest {
	if body.TenantId == nil {
		body.SetTenantId(c.effectiveDefaultTenant())
	}
	return body
}

// withDefaultTenantDecisionEvaluation injects the default tenant into whichever
// variant of the decision-evaluation union is set. The union carries pointers,
// so the variant is copied before mutation: the caller's instruction must stay
// untouched (reusing it across clients would otherwise retain the first client's
// tenant, and concurrent reuse would race).
func (c *CamundaClient) withDefaultTenantDecisionEvaluation(body DecisionEvaluationInstruction) DecisionEvaluationInstruction {
	d := c.effectiveDefaultTenant()
	if b := body.DecisionEvaluationById; b != nil && b.TenantId == nil {
		cp := *b
		cp.SetTenantId(camundaapi.MustTenantId(d))
		body.DecisionEvaluationById = &cp
	}
	if b := body.DecisionEvaluationByKey; b != nil && b.TenantId == nil {
		cp := *b
		cp.SetTenantId(camundaapi.MustTenantId(d))
		body.DecisionEvaluationByKey = &cp
	}
	return body
}

// withDefaultTenantProcessInstanceCreation injects the default tenant into
// whichever variant of the process-instance-creation union is set. The union
// carries pointers, so the variant is copied before mutation: the caller's
// instruction must stay untouched (reusing it across clients would otherwise
// retain the first client's tenant, and concurrent reuse would race).
func (c *CamundaClient) withDefaultTenantProcessInstanceCreation(body ProcessInstanceCreationInstruction) ProcessInstanceCreationInstruction {
	d := c.effectiveDefaultTenant()
	if b := body.ProcessInstanceCreationInstructionById; b != nil && b.TenantId == nil {
		cp := *b
		cp.SetTenantId(camundaapi.MustTenantId(d))
		body.ProcessInstanceCreationInstructionById = &cp
	}
	if b := body.ProcessInstanceCreationInstructionByKey; b != nil && b.TenantId == nil {
		cp := *b
		cp.SetTenantId(camundaapi.MustTenantId(d))
		body.ProcessInstanceCreationInstructionByKey = &cp
	}
	if b := body.ProcessInstanceCreationInstructionByVersionTag; b != nil && b.TenantId == nil {
		cp := *b
		cp.SetTenantId(camundaapi.MustTenantId(d))
		body.ProcessInstanceCreationInstructionByVersionTag = &cp
	}
	return body
}

// withDefaultTenantJobActivation injects the effective default tenant into a
// job-activation request when the caller left tenantIds unset (absent or
// empty) and the tenant filter is PROVIDED or unset. With tenantFilter
// ASSIGNED the server activates against the authenticated principal's assigned
// tenants and ignores tenantIds, so the request is left alone. An explicit
// tenantIds list always wins.
func (c *CamundaClient) withDefaultTenantJobActivation(body JobActivationRequest) JobActivationRequest {
	if len(body.TenantIds) > 0 {
		return body
	}
	if f := body.TenantFilter; f != nil && *f == camundaapi.TENANTFILTERENUM_ASSIGNED {
		return body
	}
	body.SetTenantIds([]camundaapi.TenantId{camundaapi.MustTenantId(c.effectiveDefaultTenant())})
	return body
}

// withDefaultTenantCreateDeployment injects the default tenant into the
// multipart deployment form when the caller (or an earlier opt) did not set one.
func (c *CamundaClient) withDefaultTenantCreateDeployment(req ApiCreateDeploymentRequest) ApiCreateDeploymentRequest {
	if req.GetTenantId() == nil {
		req = req.TenantId(c.effectiveDefaultTenant())
	}
	return req
}
