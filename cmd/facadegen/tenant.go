package main

import (
	"fmt"
	"go/ast"
	"sort"
	"strings"
)

// tenantShape describes how the facade injects the default tenant into an
// operation's JSON request body. It is derived from the body's shape in the
// generated client (which mirrors the spec schema), never from an operation
// list: a body struct with an optional top-level `TenantId *TenantId` field is
// defaulted directly; a oneOf union is defaulted in each variant that has one
// (e.g. suspendProcessDefinitions: the by-id variant has tenantId, the by-key
// variant does not and is left alone).
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

// hasOptionalTenantID reports whether st has an optional (pointer, omitempty)
// top-level `tenantId` JSON field. A required tenantId is the caller's to set.
func hasOptionalTenantID(st *ast.StructType) bool {
	for _, fld := range st.Fields.List {
		if fld.Tag == nil || len(fld.Names) != 1 {
			continue
		}
		if !strings.Contains(fld.Tag.Value, `json:"tenantId,omitempty"`) {
			continue
		}
		star, ok := fld.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if id, ok := star.X.(*ast.Ident); ok && (id.Name == "TenantId") {
			return fld.Names[0].Name == "TenantId"
		}
	}
	return false
}

// unionVariants returns the pointer-to-struct fields of an openapi-generator
// oneOf wrapper (a struct of only untagged *Variant fields), or nil.
func unionVariants(st *ast.StructType, structs map[string]*ast.StructType) []string {
	var out []string
	for _, fld := range st.Fields.List {
		if fld.Tag != nil || len(fld.Names) != 1 {
			return nil
		}
		star, ok := fld.Type.(*ast.StarExpr)
		if !ok {
			return nil
		}
		id, ok := star.X.(*ast.Ident)
		if !ok || structs[id.Name] == nil {
			return nil
		}
		out = append(out, fld.Names[0].Name)
	}
	return out
}

// tenantShapeOf derives the default-tenant injection for body, or nil when the
// body carries no optional tenantId in any variant.
func tenantShapeOf(body string, structs map[string]*ast.StructType, r *renderer) *tenantShape {
	st := structs[body]
	if st == nil {
		return nil
	}
	if hasOptionalTenantID(st) {
		return &tenantShape{bodyType: r.typeString(ast.NewIdent(body)), direct: true}
	}
	var variants []string
	for _, fld := range unionVariants(st, structs) {
		for _, f := range st.Fields.List {
			if f.Names[0].Name != fld {
				continue
			}
			vt := f.Type.(*ast.StarExpr).X.(*ast.Ident).Name
			if hasOptionalTenantID(structs[vt]) {
				variants = append(variants, fld)
			}
		}
	}
	if len(variants) == 0 {
		return nil
	}
	return &tenantShape{bodyType: r.typeString(ast.NewIdent(body)), variants: variants}
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
