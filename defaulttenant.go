package camunda

import (
	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
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
// An explicitly set tenant always wins. Bodies with an optional top-level
// tenantId (directly or in a oneOf variant) get generated withDefaultTenant<Body>
// helpers in facade_generated.go; the helpers here cover what that structural
// rule cannot: CreateProcessInstance (hand-written for FALCON), ActivateJobs
// (tenantIds list + tenantFilter) and CreateDeployment (multipart form field).

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

// defaultTenantIDs applies the default-tenant rule shared by every activation
// request whose body carries an optional tenantIds list: when the caller left
// tenantIds unset (absent or empty) and the tenant filter is PROVIDED or unset,
// it returns the effective default tenant as the list to send. With tenantFilter
// ASSIGNED the server activates against the authenticated principal's assigned
// tenants and ignores tenantIds, so the list is left alone. An explicit
// tenantIds list always wins. The second result reports whether to replace the
// list.
func (c *CamundaClient) defaultTenantIDs(ids []camundaapi.TenantId, filter *camundaapi.TenantFilterEnum) ([]camundaapi.TenantId, bool) {
	if len(ids) > 0 {
		return nil, false
	}
	if filter != nil && *filter == camundaapi.TENANTFILTERENUM_ASSIGNED {
		return nil, false
	}
	return []camundaapi.TenantId{camundaapi.MustTenantId(c.effectiveDefaultTenant())}, true
}

// withDefaultTenantJobActivation injects the effective default tenant into a
// job-activation request (see defaultTenantIDs for the rule).
func (c *CamundaClient) withDefaultTenantJobActivation(body JobActivationRequest) JobActivationRequest {
	if ids, ok := c.defaultTenantIDs(body.TenantIds, body.TenantFilter); ok {
		body.SetTenantIds(ids)
	}
	return body
}

// withDefaultTenantProbeActivation injects the effective default tenant into a
// probe-activation request (see defaultTenantIDs for the rule). The facade
// calls it for ActivateProbes because the spec metadata flags that operation
// optionalTenantIdsInBody.
func (c *CamundaClient) withDefaultTenantProbeActivation(body ProbeActivationRequest) ProbeActivationRequest {
	if ids, ok := c.defaultTenantIDs(body.TenantIds, body.TenantFilter); ok {
		body.SetTenantIds(ids)
	}
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
