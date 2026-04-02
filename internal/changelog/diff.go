package changelog

import (
	"fmt"
	"sort"
	"strings"
)

// ChangeKind classifies a single detected difference.
type ChangeKind string

const (
	KindTypeAdded   ChangeKind = "type-added"
	KindTypeRemoved ChangeKind = "type-removed"

	KindFieldAdded        ChangeKind = "field-added"
	KindFieldRemoved      ChangeKind = "field-removed"
	KindFieldTypeChanged  ChangeKind = "field-type-changed"
	KindFieldBecameOpt    ChangeKind = "field-became-optional"
	KindFieldBecameReq    ChangeKind = "field-became-required"
	KindFieldTagChanged   ChangeKind = "field-tag-changed"

	KindEnumAdded         ChangeKind = "enum-type-added"
	KindEnumRemoved       ChangeKind = "enum-type-removed"
	KindEnumMemberAdded   ChangeKind = "enum-member-added"
	KindEnumMemberRemoved ChangeKind = "enum-member-removed"

	KindAliasAdded        ChangeKind = "alias-added"
	KindAliasRemoved      ChangeKind = "alias-removed"
	KindAliasTargetChanged ChangeKind = "alias-target-changed"
)

// Severity of a change from the API consumer's perspective.
type Severity string

const (
	SeverityBreaking Severity = "breaking"
	SeverityWarning  Severity = "warning"
	SeverityAdditive Severity = "additive"
	SeverityInfo     Severity = "info"
)

// Change represents a single detected difference between two versions.
type Change struct {
	Kind     ChangeKind
	Severity Severity
	Type     string // The type name involved
	Field    string // The field name (if applicable)
	Old      string // Old value/type
	New      string // New value/type
	Detail   string // Human-readable description
	Role     TypeRole // request/response/unknown
}

// DiffResult holds all changes between two package versions.
type DiffResult struct {
	OldVersion string
	NewVersion string
	Changes    []Change
}

// Diff compares two PackageInfos and returns all detected changes.
func Diff(old, new_ *PackageInfo, oldVersion, newVersion string, roles map[string]TypeRole) *DiffResult {
	result := &DiffResult{
		OldVersion: oldVersion,
		NewVersion: newVersion,
	}

	diffStructs(old, new_, roles, result)
	diffEnums(old, new_, roles, result)
	diffAliases(old, new_, roles, result)

	// Sort changes: breaking first, then by type name
	sort.Slice(result.Changes, func(i, j int) bool {
		a, b := result.Changes[i], result.Changes[j]
		if a.Severity != b.Severity {
			return severityOrder(a.Severity) < severityOrder(b.Severity)
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.Field < b.Field
	})

	return result
}

func severityOrder(s Severity) int {
	switch s {
	case SeverityBreaking:
		return 0
	case SeverityWarning:
		return 1
	case SeverityAdditive:
		return 2
	case SeverityInfo:
		return 3
	default:
		return 4
	}
}

func diffStructs(old, new_ *PackageInfo, roles map[string]TypeRole, result *DiffResult) {
	// Removed structs
	for name, s := range old.Structs {
		if _, ok := new_.Structs[name]; !ok {
			result.Changes = append(result.Changes, Change{
				Kind:     KindTypeRemoved,
				Severity: SeverityBreaking,
				Type:     name,
				Old:      describeStruct(s),
				Detail:   "struct `" + name + "` removed",
				Role:     lookupRole(name, roles),
			})
		}
	}

	// Added structs
	for name, s := range new_.Structs {
		if _, ok := old.Structs[name]; !ok {
			result.Changes = append(result.Changes, Change{
				Kind:     KindTypeAdded,
				Severity: SeverityAdditive,
				Type:     name,
				New:      describeStruct(s),
				Detail:   "struct `" + name + "` added",
				Role:     lookupRole(name, roles),
			})
		}
	}

	// Changed structs
	for name, oldS := range old.Structs {
		newS, ok := new_.Structs[name]
		if !ok {
			continue
		}
		role := lookupRole(name, roles)
		diffStructFields(name, oldS, newS, role, result)
	}
}

func diffStructFields(typeName string, old, new_ *StructInfo, role TypeRole, result *DiffResult) {
	oldFields := make(map[string]*FieldInfo)
	for i := range old.Fields {
		oldFields[old.Fields[i].Name] = &old.Fields[i]
	}

	newFields := make(map[string]*FieldInfo)
	for i := range new_.Fields {
		newFields[new_.Fields[i].Name] = &new_.Fields[i]
	}

	// Removed fields
	for name, f := range oldFields {
		if _, ok := newFields[name]; !ok {
			sev := SeverityBreaking
			if role == RoleRequest && f.Optional {
				sev = SeverityWarning // removing an optional request field is less severe
			}
			result.Changes = append(result.Changes, Change{
				Kind:     KindFieldRemoved,
				Severity: sev,
				Type:     typeName,
				Field:    name,
				Old:      f.TypeExpr,
				Detail:   "`" + typeName + "." + name + "` removed (was `" + f.TypeExpr + "`)",
				Role:     role,
			})
		}
	}

	// Added fields
	for name, f := range newFields {
		if _, ok := oldFields[name]; !ok {
			sev := SeverityAdditive
			if role == RoleRequest && !f.Optional {
				sev = SeverityBreaking // new required request field is breaking
			}
			result.Changes = append(result.Changes, Change{
				Kind:     KindFieldAdded,
				Severity: sev,
				Type:     typeName,
				Field:    name,
				New:      f.TypeExpr,
				Detail:   "`" + typeName + "." + name + "` added (`" + f.TypeExpr + "`)",
				Role:     role,
			})
		}
	}

	// Changed fields
	for name, oldF := range oldFields {
		newF, ok := newFields[name]
		if !ok {
			continue
		}

		// Type changed
		if oldF.TypeExpr != newF.TypeExpr {
			// Check if only optionality changed (pointer added/removed)
			oldBase := strings.TrimPrefix(oldF.TypeExpr, "*")
			newBase := strings.TrimPrefix(newF.TypeExpr, "*")

			if oldBase == newBase {
				// Only pointer wrapper changed
				if !oldF.Optional && newF.Optional {
					sev := SeverityInfo
					if role == RoleResponse {
						sev = SeverityWarning // response field became nullable
					}
					result.Changes = append(result.Changes, Change{
						Kind:     KindFieldBecameOpt,
						Severity: sev,
						Type:     typeName,
						Field:    name,
						Old:      oldF.TypeExpr,
						New:      newF.TypeExpr,
						Detail:   "`" + typeName + "." + name + "` became optional (`" + oldF.TypeExpr + "` → `" + newF.TypeExpr + "`)",
						Role:     role,
					})
				} else if oldF.Optional && !newF.Optional {
					sev := SeverityInfo
					if role == RoleRequest {
						sev = SeverityBreaking // request field became required
					}
					result.Changes = append(result.Changes, Change{
						Kind:     KindFieldBecameReq,
						Severity: sev,
						Type:     typeName,
						Field:    name,
						Old:      oldF.TypeExpr,
						New:      newF.TypeExpr,
						Detail:   "`" + typeName + "." + name + "` became required (`" + oldF.TypeExpr + "` → `" + newF.TypeExpr + "`)",
						Role:     role,
					})
				}
			} else {
				result.Changes = append(result.Changes, Change{
					Kind:     KindFieldTypeChanged,
					Severity: SeverityBreaking,
					Type:     typeName,
					Field:    name,
					Old:      oldF.TypeExpr,
					New:      newF.TypeExpr,
					Detail:   "`" + typeName + "." + name + "`: type changed (`" + oldF.TypeExpr + "` → `" + newF.TypeExpr + "`)",
					Role:     role,
				})
			}
		}

		// JSON tag changed (without type change)
		if oldF.JSONName != newF.JSONName && oldF.JSONName != "" && newF.JSONName != "" {
			result.Changes = append(result.Changes, Change{
				Kind:     KindFieldTagChanged,
				Severity: SeverityBreaking,
				Type:     typeName,
				Field:    name,
				Old:      oldF.JSONName,
				New:      newF.JSONName,
				Detail:   "`" + typeName + "." + name + "`: JSON name changed (`" + oldF.JSONName + "` → `" + newF.JSONName + "`)",
				Role:     role,
			})
		}
	}
}

func diffEnums(old, new_ *PackageInfo, roles map[string]TypeRole, result *DiffResult) {
	for name := range old.Enums {
		if _, ok := new_.Enums[name]; !ok {
			result.Changes = append(result.Changes, Change{
				Kind:     KindEnumRemoved,
				Severity: SeverityBreaking,
				Type:     name,
				Detail:   "enum type `" + name + "` removed",
				Role:     lookupRole(name, roles),
			})
		}
	}

	for name := range new_.Enums {
		if _, ok := old.Enums[name]; !ok {
			result.Changes = append(result.Changes, Change{
				Kind:     KindEnumAdded,
				Severity: SeverityAdditive,
				Type:     name,
				Detail:   "enum type `" + name + "` added",
				Role:     lookupRole(name, roles),
			})
		}
	}

	for name, oldE := range old.Enums {
		newE, ok := new_.Enums[name]
		if !ok {
			continue
		}
		role := lookupRole(name, roles)
		diffEnumValues(name, oldE, newE, role, result)
	}
}

func diffEnumValues(typeName string, old, new_ *EnumInfo, role TypeRole, result *DiffResult) {
	oldVals := make(map[string]string) // value → const name
	for _, v := range old.Values {
		oldVals[v.Value] = v.Name
	}

	newVals := make(map[string]string)
	for _, v := range new_.Values {
		newVals[v.Value] = v.Name
	}

	for val := range oldVals {
		if _, ok := newVals[val]; !ok {
			result.Changes = append(result.Changes, Change{
				Kind:     KindEnumMemberRemoved,
				Severity: SeverityBreaking,
				Type:     typeName,
				Old:      val,
				Detail:   "`" + typeName + "` lost member `\"" + val + "\"`",
				Role:     role,
			})
		}
	}

	for val := range newVals {
		if _, ok := oldVals[val]; !ok {
			sev := SeverityAdditive
			result.Changes = append(result.Changes, Change{
				Kind:     KindEnumMemberAdded,
				Severity: sev,
				Type:     typeName,
				New:      val,
				Detail:   "`" + typeName + "` gained member `\"" + val + "\"`",
				Role:     role,
			})
		}
	}
}

func diffAliases(old, new_ *PackageInfo, roles map[string]TypeRole, result *DiffResult) {
	for name, a := range old.Aliases {
		if _, ok := new_.Aliases[name]; !ok {
			// Check if it became a struct or enum
			if _, ok := new_.Structs[name]; ok {
				result.Changes = append(result.Changes, Change{
					Kind:     KindAliasRemoved,
					Severity: SeverityBreaking,
					Type:     name,
					Old:      a.Target,
					Detail:   "`" + name + "` changed from alias (`= " + a.Target + "`) to struct",
					Role:     lookupRole(name, roles),
				})
			} else {
				result.Changes = append(result.Changes, Change{
					Kind:     KindAliasRemoved,
					Severity: SeverityBreaking,
					Type:     name,
					Old:      a.Target,
					Detail:   "type alias `" + name + "` removed (was `= " + a.Target + "`)",
					Role:     lookupRole(name, roles),
				})
			}
		}
	}

	for name, a := range new_.Aliases {
		if _, ok := old.Aliases[name]; !ok {
			if _, ok := old.Structs[name]; ok {
				continue // was a struct, now an alias — handled in struct removal
			}
			if _, ok := old.Enums[name]; ok {
				continue // was an enum, now an alias
			}
			result.Changes = append(result.Changes, Change{
				Kind:     KindAliasAdded,
				Severity: SeverityAdditive,
				Type:     name,
				New:      a.Target,
				Detail:   "type alias `" + name + "` added (`= " + a.Target + "`)",
				Role:     lookupRole(name, roles),
			})
		}
	}

	for name, oldA := range old.Aliases {
		newA, ok := new_.Aliases[name]
		if !ok {
			continue
		}
		if oldA.Target != newA.Target {
			result.Changes = append(result.Changes, Change{
				Kind:     KindAliasTargetChanged,
				Severity: SeverityBreaking,
				Type:     name,
				Old:      oldA.Target,
				New:      newA.Target,
				Detail:   "alias `" + name + "` target changed (`" + oldA.Target + "` → `" + newA.Target + "`)",
				Role:     lookupRole(name, roles),
			})
		}
	}
}

func describeStruct(s *StructInfo) string {
	return fmt.Sprintf("struct{%d fields}", len(s.Fields))
}

func lookupRole(name string, roles map[string]TypeRole) TypeRole {
	if r, ok := roles[name]; ok {
		return r
	}
	return ClassifyRole(name)
}
