package camunda

import (
	camundaapi "github.com/camunda/orchestration-cluster-api-go/v11/client"
)

// defaultTenant returns the configured default tenant id, or "" when none is
// configured (injection is then a no-op and the server-side default applies).
func (c *CamundaClient) defaultTenant() string { return c.cfg.DefaultTenantID }

// tenantIDOrDefault converts an optional branded TenantId into the plain *string
// the falcon producer and resolveTenant operate on, applying the configured
// default tenant when the instruction did not specify one.
func (c *CamundaClient) tenantIDOrDefault(t *camundaapi.TenantId) *string {
	if t != nil {
		s := string(*t)
		return &s
	}
	if d := c.cfg.DefaultTenantID; d != "" {
		return &d
	}
	return nil
}

// The withDefaultTenant* helpers implement WithDefaultTenantID /
// CAMUNDA_DEFAULT_TENANT_ID for operations whose tenant id is an optional field
// of the request body (or, for CreateDeployment, an optional multipart form
// parameter): when the caller omitted the tenant, the configured default is
// injected so the request matches the other Camunda SDKs. An explicitly set
// tenant always wins, and with no configured default the request is sent
// unchanged. The generated facade calls these for the tenant-accepting
// operations; see defaultTenantBodyOps in cmd/facadegen.

func (c *CamundaClient) withDefaultTenantMessagePublication(body MessagePublicationRequest) MessagePublicationRequest {
	if body.TenantId == nil {
		if d := c.defaultTenant(); d != "" {
			body.SetTenantId(camundaapi.MustTenantId(d))
		}
	}
	return body
}

func (c *CamundaClient) withDefaultTenantMessageCorrelation(body MessageCorrelationRequest) MessageCorrelationRequest {
	if body.TenantId == nil {
		if d := c.defaultTenant(); d != "" {
			body.SetTenantId(camundaapi.MustTenantId(d))
		}
	}
	return body
}

func (c *CamundaClient) withDefaultTenantSignalBroadcast(body SignalBroadcastRequest) SignalBroadcastRequest {
	if body.TenantId == nil {
		if d := c.defaultTenant(); d != "" {
			body.SetTenantId(camundaapi.MustTenantId(d))
		}
	}
	return body
}

func (c *CamundaClient) withDefaultTenantConditionalEvaluation(body ConditionalEvaluationInstruction) ConditionalEvaluationInstruction {
	if body.TenantId == nil {
		if d := c.defaultTenant(); d != "" {
			body.SetTenantId(camundaapi.MustTenantId(d))
		}
	}
	return body
}

func (c *CamundaClient) withDefaultTenantExpressionEvaluation(body ExpressionEvaluationRequest) ExpressionEvaluationRequest {
	if body.TenantId == nil {
		if d := c.defaultTenant(); d != "" {
			body.SetTenantId(d)
		}
	}
	return body
}

// withDefaultTenantDecisionEvaluation injects the default tenant into whichever
// variant of the decision-evaluation union is set.
func (c *CamundaClient) withDefaultTenantDecisionEvaluation(body DecisionEvaluationInstruction) DecisionEvaluationInstruction {
	d := c.defaultTenant()
	if d == "" {
		return body
	}
	if b := body.DecisionEvaluationById; b != nil && b.TenantId == nil {
		b.SetTenantId(camundaapi.MustTenantId(d))
	}
	if b := body.DecisionEvaluationByKey; b != nil && b.TenantId == nil {
		b.SetTenantId(camundaapi.MustTenantId(d))
	}
	return body
}

// withDefaultTenantProcessInstanceCreation injects the default tenant into
// whichever variant of the process-instance-creation union is set.
func (c *CamundaClient) withDefaultTenantProcessInstanceCreation(body ProcessInstanceCreationInstruction) ProcessInstanceCreationInstruction {
	d := c.defaultTenant()
	if d == "" {
		return body
	}
	if b := body.ProcessInstanceCreationInstructionById; b != nil && b.TenantId == nil {
		b.SetTenantId(camundaapi.MustTenantId(d))
	}
	if b := body.ProcessInstanceCreationInstructionByKey; b != nil && b.TenantId == nil {
		b.SetTenantId(camundaapi.MustTenantId(d))
	}
	return body
}

// withDefaultTenantCreateDeployment injects the default tenant into the
// multipart deployment form when the caller (or an earlier opt) did not set one.
func (c *CamundaClient) withDefaultTenantCreateDeployment(req ApiCreateDeploymentRequest) ApiCreateDeploymentRequest {
	if d := c.defaultTenant(); d != "" && req.GetTenantId() == nil {
		req = req.TenantId(d)
	}
	return req
}
