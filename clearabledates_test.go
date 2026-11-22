package camunda

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// clearByEmptyString matches the spec's prose for a property cleared by "": both
// the schema-level bullet ("* `dueDate` - reset by providing an empty String") and
// the property description ("Clear by providing an empty String.").
var clearByEmptyString = regexp.MustCompile(`(?i)(?:clear|reset) by providing an empty string`)

// clearableDatesInSpec re-derives, independently of hook 09, every date property the
// bundled spec says is cleared by an empty string: one documented so in prose, or
// one typed by an `x-clearable` schema ($ref, or a single-element allOf).
func clearableDatesInSpec(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(bundledSpecPath)
	if err != nil {
		t.Fatalf("read bundled spec: %v", err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Description string                    `json:"description"`
				Properties  map[string]map[string]any `json:"properties"`
				XClearable  bool                      `json:"x-clearable"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse bundled spec: %v", err)
	}
	schemas := spec.Components.Schemas
	clearableRef := func(prop map[string]any) bool {
		ref, _ := prop["$ref"].(string)
		if all, ok := prop["allOf"].([]any); ok && len(all) == 1 {
			if m, ok := all[0].(map[string]any); ok {
				ref, _ = m["$ref"].(string)
			}
		}
		return ref != "" && schemas[strings.TrimPrefix(ref, "#/components/schemas/")].XClearable
	}

	var out []string
	for name, schema := range schemas {
		for prop, def := range schema.Properties {
			isDate := def["format"] == "date-time" || clearableRef(def)
			desc, _ := def["description"].(string)
			bullet := regexp.MustCompile("(?i)\\*\\s*`" + regexp.QuoteMeta(prop) + "`\\s*-\\s*(?:clear|reset) by providing an empty string")
			documented := clearByEmptyString.MatchString(desc) || bullet.MatchString(schema.Description)
			if isDate && (documented || clearableRef(def) || def["x-clearable"] == true) {
				out = append(out, name+"."+prop)
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestClearableDatesHaveClearAccessors guards the whole class of "the API clears this
// date with an empty string, but the SDK cannot send one": every such property must
// stay a NullableTime with the Clear/IsCleared pair hook 09 adds. It fails when the
// spec changes how it declares a clearable date and the hook stops recognising it.
func TestClearableDatesHaveClearAccessors(t *testing.T) {
	props := clearableDatesInSpec(t)
	if len(props) == 0 {
		t.Fatal("found no clearable dates in the bundled spec; the guard would pass vacuously")
	}
	files, err := filepath.Glob("client/model_*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob client models: %v (%d files)", err, len(files))
	}
	var src strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		src.Write(b)
	}
	code := src.String()
	for _, p := range props {
		model, prop, _ := strings.Cut(p, ".")
		field := strings.ToUpper(prop[:1]) + prop[1:]
		for _, want := range []string{
			"\t" + field + " NullableTime `json:\"" + prop + ",omitempty\"`",
			"func (o *" + model + ") Clear" + field + "() {",
			"func (o *" + model + ") Is" + field + "Cleared() bool {",
		} {
			if !strings.Contains(code, want) {
				t.Errorf("%s is cleared by an empty string, but client/ lacks %q", p, want)
			}
		}
	}
}
