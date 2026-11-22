package main

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
)

// tenantShape describes how the facade injects the default tenant into an
// operation's JSON request body. It comes from the spec metadata
// (optionalTenantIdInBodyVariants), never from an operation list: a body
// schema with an optional top-level tenantId is defaulted directly; a oneOf
// union is defaulted in each listed variant (e.g. suspendProcessDefinitions:
// the by-id variant has tenantId, the by-key variant does not and is left
// alone).
type tenantShape struct {
	bodyType string   // rendered body type
	direct   bool     // the body itself has an optional TenantId
	variants []string // union fields (pointer-to-variant) whose variant has an optional TenantId
}

// collectStructs indexes every struct type declared in the client package.
func collectStructs(files []*ast.File) map[string]*ast.StructType {
	out := map[string]*ast.StructType{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if st, ok := ts.Type.(*ast.StructType); ok {
					out[ts.Name.Name] = st
				}
			}
		}
	}
	return out
}

// tenantShapeOf builds the default-tenant injection for body from the spec
// metadata's optionalTenantIdInBodyVariants (camunda-schema-bundler >= 2.5.0):
// the request-body schema names with an optional top-level tenantId. When the
// body schema itself is listed, the body is defaulted directly; otherwise each
// listed name is a oneOf variant, which openapi-generator emits as a
// same-named pointer field on the union wrapper. Returns nil when nothing is
// listed, and an error when a listed variant is not a field of the body (the
// metadata and the generated client disagree — never guess).
func tenantShapeOf(body string, variants []string, structs map[string]*ast.StructType, r *renderer) (*tenantShape, error) {
	if len(variants) == 0 {
		return nil, nil
	}
	shape := &tenantShape{bodyType: r.typeString(ast.NewIdent(body))}
	for _, v := range variants {
		if v == body {
			return &tenantShape{bodyType: shape.bodyType, direct: true}, nil
		}
	}
	st := structs[body]
	if st == nil {
		return nil, fmt.Errorf("tenant body %s: no such struct in the client", body)
	}
	for _, v := range variants {
		if !hasUnionField(st, v) {
			return nil, fmt.Errorf("tenant body %s: metadata variant %s is not a union field", body, v)
		}
		shape.variants = append(shape.variants, v)
	}
	return shape, nil
}

// hasUnionField reports whether st has an untagged *name field named name
// (the openapi-generator oneOf wrapper shape).
func hasUnionField(st *ast.StructType, name string) bool {
	for _, fld := range st.Fields.List {
		if fld.Tag != nil || len(fld.Names) != 1 || fld.Names[0].Name != name {
			continue
		}
		if star, ok := fld.Type.(*ast.StarExpr); ok {
			if id, ok := star.X.(*ast.Ident); ok && id.Name == name {
				return true
			}
		}
	}
	return false
}

// tenantHelper names the CamundaClient method that defaults the tenant of this
// operation's body, or "" when the body takes no default tenant.
func (op operation) tenantHelper() string {
	if op.tenantHook != "" {
		return op.tenantHook
	}
	if op.tenant != nil {
		return "withDefaultTenant" + op.bodyBuilder
	}
	return ""
}

// emitTenantHelpers writes one derived withDefaultTenant<Body> helper per body
// type that needs one.
func emitTenantHelpers(b *strings.Builder, ops []operation) {
	byBody := map[string]*tenantShape{}
	for _, op := range ops {
		if op.tenant != nil && op.tenantHook == "" && op.bodyBuilder != "" {
			byBody[op.bodyBuilder] = op.tenant
		}
	}
	names := make([]string, 0, len(byBody))
	for n := range byBody {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ts := byBody[n]
		fmt.Fprintf(b, "// withDefaultTenant%s injects the effective default tenant (see\n// effectiveDefaultTenant) when the caller left tenantId unset. An explicit\n// tenant wins. Derived from the request-body shape.\n", n)
		fmt.Fprintf(b, "func (c *CamundaClient) withDefaultTenant%s(body %s) %s {\n", n, ts.bodyType, ts.bodyType)
		if ts.direct {
			b.WriteString("\tif body.TenantId == nil {\n\t\tt := TenantId(c.effectiveDefaultTenant())\n\t\tbody.TenantId = &t\n\t}\n")
		} else {
			// Union variants are pointers: copy before mutating so the caller's
			// instruction stays untouched (safe reuse across clients/goroutines).
			for _, v := range ts.variants {
				fmt.Fprintf(b, "\tif v := body.%s; v != nil && v.TenantId == nil {\n\t\tcp := *v\n\t\tt := TenantId(c.effectiveDefaultTenant())\n\t\tcp.TenantId = &t\n\t\tbody.%s = &cp\n\t}\n", v, v)
			}
		}
		b.WriteString("\treturn body\n}\n\n")
	}
}
