// Package changelog provides Go type introspection and structural diffing
// for oapi-codegen generated API clients.
package changelog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
)

// FieldInfo describes a single struct field.
type FieldInfo struct {
	Name     string // Go field name
	TypeExpr string // Type as written in source (e.g. "*string", "ProcessDefinitionKey")
	JSONName string // JSON tag name (from `json:"..."`)
	Optional bool   // True if pointer type (*T) or has omitempty
	Tag      string // Full struct tag
	Comment  string // Doc comment
}

// StructInfo describes an exported struct type.
type StructInfo struct {
	Name    string
	Fields  []FieldInfo
	Comment string
}

// EnumValue describes a single const in an enum group.
type EnumValue struct {
	Name  string // Go const name
	Value string // String literal value
}

// EnumInfo describes a typed-string enum (type X string + const group).
type EnumInfo struct {
	Name   string      // Type name
	Values []EnumValue // Known const values
}

// AliasInfo describes a type alias (type X = Y).
type AliasInfo struct {
	Name   string
	Target string // The aliased type as written in source
}

// PackageInfo holds the parsed API surface of a single generated package.
type PackageInfo struct {
	Structs map[string]*StructInfo
	Enums   map[string]*EnumInfo
	Aliases map[string]*AliasInfo
}

// ParseFile parses a single Go source file and extracts all exported
// structs, enums, and type aliases. Designed for oapi-codegen output.
func ParseFile(path string) (*PackageInfo, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	info := &PackageInfo{
		Structs: make(map[string]*StructInfo),
		Enums:   make(map[string]*EnumInfo),
		Aliases: make(map[string]*AliasInfo),
	}

	// First pass: collect type declarations
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}

		if gd.Tok == token.TYPE {
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				name := ts.Name.Name
				if !ast.IsExported(name) {
					continue
				}

				comment := ""
				if gd.Doc != nil {
					comment = strings.TrimSpace(gd.Doc.Text())
				}

				switch t := ts.Type.(type) {
				case *ast.StructType:
					info.Structs[name] = parseStruct(name, t, comment)
				case *ast.Ident:
					if ts.Assign != 0 {
						// type X = Y (alias)
						info.Aliases[name] = &AliasInfo{Name: name, Target: t.Name}
					} else {
						// type X string (enum base)
						info.Enums[name] = &EnumInfo{Name: name}
					}
				case *ast.SelectorExpr:
					if ts.Assign != 0 {
						info.Aliases[name] = &AliasInfo{
							Name:   name,
							Target: exprToString(t),
						}
					}
				}
			}
		}
	}

	// Second pass: collect const values for enum types
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}

		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			if vs.Type == nil || len(vs.Names) == 0 || len(vs.Values) == 0 {
				continue
			}

			typeName := exprToString(vs.Type)
			enum, ok := info.Enums[typeName]
			if !ok {
				continue
			}

			for i, name := range vs.Names {
				if i < len(vs.Values) {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						val := strings.Trim(lit.Value, `"`)
						enum.Values = append(enum.Values, EnumValue{
							Name:  name.Name,
							Value: val,
						})
					}
				}
			}
		}
	}

	return info, nil
}

func parseStruct(name string, st *ast.StructType, comment string) *StructInfo {
	s := &StructInfo{
		Name:    name,
		Comment: comment,
	}

	if st.Fields == nil {
		return s
	}

	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			// Embedded field — skip for now
			continue
		}

		for _, ident := range field.Names {
			if !ast.IsExported(ident.Name) {
				continue
			}

			fi := FieldInfo{
				Name:     ident.Name,
				TypeExpr: exprToString(field.Type),
			}

			if field.Tag != nil {
				fi.Tag = strings.Trim(field.Tag.Value, "`")
				tag := reflect.StructTag(fi.Tag)
				jsonTag := tag.Get("json")
				if jsonTag != "" {
					parts := strings.Split(jsonTag, ",")
					fi.JSONName = parts[0]
					for _, p := range parts[1:] {
						if p == "omitempty" {
							fi.Optional = true
						}
					}
				}
			}

			// Pointer types are optional
			if strings.HasPrefix(fi.TypeExpr, "*") {
				fi.Optional = true
			}

			if field.Doc != nil {
				fi.Comment = strings.TrimSpace(field.Doc.Text())
			}

			s.Fields = append(s.Fields, fi)
		}
	}

	return s
}

// exprToString renders an ast.Expr back to its source representation.
func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprToString(e.X)
	case *ast.SelectorExpr:
		return exprToString(e.X) + "." + e.Sel.Name
	case *ast.ArrayType:
		if e.Len == nil {
			return "[]" + exprToString(e.Elt)
		}
		return "[...]" + exprToString(e.Elt)
	case *ast.MapType:
		return "map[" + exprToString(e.Key) + "]" + exprToString(e.Value)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return "?"
	}
}

// SortedStructNames returns sorted struct names from a PackageInfo.
func SortedStructNames(p *PackageInfo) []string {
	names := make([]string, 0, len(p.Structs))
	for name := range p.Structs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SortedEnumNames returns sorted enum type names from a PackageInfo.
func SortedEnumNames(p *PackageInfo) []string {
	names := make([]string, 0, len(p.Enums))
	for name := range p.Enums {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SortedAliasNames returns sorted alias names from a PackageInfo.
func SortedAliasNames(p *PackageInfo) []string {
	names := make([]string, 0, len(p.Aliases))
	for name := range p.Aliases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
