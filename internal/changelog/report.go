package changelog

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// --- Markdown Report ---

// GenerateMarkdown produces a Markdown changelog from a DiffResult.
func GenerateMarkdown(r *DiffResult) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("# API Changelog: %s → %s\n\n", r.OldVersion, r.NewVersion))

	stats := computeStats(r)
	b.WriteString(fmt.Sprintf("> **%d** breaking, **%d** warning, **%d** additive, **%d** info — **%d** total changes\n\n",
		stats.Breaking, stats.Warning, stats.Additive, stats.Info, stats.Total))

	// Group by severity
	breaking := filterBySeverity(r.Changes, SeverityBreaking)
	warnings := filterBySeverity(r.Changes, SeverityWarning)
	additive := filterBySeverity(r.Changes, SeverityAdditive)
	info := filterBySeverity(r.Changes, SeverityInfo)

	if len(breaking) > 0 {
		writeSection(&b, "🔴 Breaking Changes", breaking)
	}
	if len(warnings) > 0 {
		writeSection(&b, "🟡 Warnings", warnings)
	}
	if len(additive) > 0 {
		writeSection(&b, "🟢 Additive Changes", additive)
	}
	if len(info) > 0 {
		writeSection(&b, "ℹ️ Info", info)
	}

	if stats.Total == 0 {
		b.WriteString("No changes detected.\n")
	}

	// Summary tables
	writeSummaryTable(&b, r)

	return b.String()
}

func writeSection(b *strings.Builder, title string, changes []Change) {
	b.WriteString(fmt.Sprintf("## %s\n\n", title))

	// Group by type name
	groups := groupByType(changes)
	typeNames := make([]string, 0, len(groups))
	for name := range groups {
		typeNames = append(typeNames, name)
	}
	sort.Strings(typeNames)

	for _, typeName := range typeNames {
		items := groups[typeName]
		role := items[0].Role
		roleTag := ""
		if role == RoleRequest {
			roleTag = " `[request]`"
		} else if role == RoleResponse {
			roleTag = " `[response]`"
		}

		b.WriteString(fmt.Sprintf("### `%s`%s\n\n", typeName, roleTag))

		for _, c := range items {
			icon := kindIcon(c.Kind)
			b.WriteString(fmt.Sprintf("- %s %s\n", icon, c.Detail))
		}
		b.WriteString("\n")
	}
}

func kindIcon(k ChangeKind) string {
	switch k {
	case KindTypeAdded, KindEnumAdded, KindAliasAdded:
		return "➕"
	case KindTypeRemoved, KindEnumRemoved, KindAliasRemoved:
		return "❌"
	case KindFieldAdded:
		return "➕"
	case KindFieldRemoved:
		return "❌"
	case KindFieldTypeChanged, KindAliasTargetChanged:
		return "🔄"
	case KindFieldBecameOpt:
		return "◻️"
	case KindFieldBecameReq:
		return "◼️"
	case KindFieldTagChanged:
		return "🏷️"
	case KindEnumMemberAdded:
		return "➕"
	case KindEnumMemberRemoved:
		return "❌"
	default:
		return "•"
	}
}

func groupByType(changes []Change) map[string][]Change {
	groups := make(map[string][]Change)
	for _, c := range changes {
		groups[c.Type] = append(groups[c.Type], c)
	}
	return groups
}

type stats struct {
	Breaking int
	Warning  int
	Additive int
	Info     int
	Total    int
}

func computeStats(r *DiffResult) stats {
	var s stats
	for _, c := range r.Changes {
		switch c.Severity {
		case SeverityBreaking:
			s.Breaking++
		case SeverityWarning:
			s.Warning++
		case SeverityAdditive:
			s.Additive++
		case SeverityInfo:
			s.Info++
		}
	}
	s.Total = len(r.Changes)
	return s
}

func filterBySeverity(changes []Change, sev Severity) []Change {
	var out []Change
	for _, c := range changes {
		if c.Severity == sev {
			out = append(out, c)
		}
	}
	return out
}

func writeSummaryTable(b *strings.Builder, r *DiffResult) {
	// Count by kind
	kindCounts := make(map[ChangeKind]int)
	for _, c := range r.Changes {
		kindCounts[c.Kind]++
	}

	if len(kindCounts) == 0 {
		return
	}

	b.WriteString("---\n\n## Summary\n\n")
	b.WriteString("| Change Kind | Count |\n")
	b.WriteString("|---|---|\n")

	kinds := make([]ChangeKind, 0, len(kindCounts))
	for k := range kindCounts {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return string(kinds[i]) < string(kinds[j]) })

	for _, k := range kinds {
		b.WriteString(fmt.Sprintf("| `%s` | %d |\n", k, kindCounts[k]))
	}
	b.WriteString("\n")
}

// --- JSON Report ---

// JSONReport is the structured JSON output format.
type JSONReport struct {
	OldVersion string        `json:"oldVersion"`
	NewVersion string        `json:"newVersion"`
	Stats      JSONStats     `json:"stats"`
	Changes    []JSONChange  `json:"changes"`
}

type JSONStats struct {
	Breaking int `json:"breaking"`
	Warning  int `json:"warning"`
	Additive int `json:"additive"`
	Info     int `json:"info"`
	Total    int `json:"total"`
}

type JSONChange struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Type     string `json:"type"`
	Field    string `json:"field,omitempty"`
	Old      string `json:"old,omitempty"`
	New      string `json:"new,omitempty"`
	Detail   string `json:"detail"`
	Role     string `json:"role"`
}

// GenerateJSON produces a JSON changelog from a DiffResult.
func GenerateJSON(r *DiffResult) (string, error) {
	s := computeStats(r)

	report := JSONReport{
		OldVersion: r.OldVersion,
		NewVersion: r.NewVersion,
		Stats: JSONStats{
			Breaking: s.Breaking,
			Warning:  s.Warning,
			Additive: s.Additive,
			Info:     s.Info,
			Total:    s.Total,
		},
	}

	for _, c := range r.Changes {
		report.Changes = append(report.Changes, JSONChange{
			Kind:     string(c.Kind),
			Severity: string(c.Severity),
			Type:     c.Type,
			Field:    c.Field,
			Old:      c.Old,
			New:      c.New,
			Detail:   c.Detail,
			Role:     string(c.Role),
		})
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}
