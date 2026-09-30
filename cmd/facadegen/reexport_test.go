package main

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateReexports locks each declaration shape the re-export generator
// handles against the fixture client, and that plumbing stays out.
func TestGenerateReexports(t *testing.T) {
	src, count, err := generateReexports("testdata/client", t.TempDir())
	if err != nil {
		t.Fatalf("generateReexports: %v", err)
	}
	if _, err := format.Source([]byte(src)); err != nil {
		t.Fatalf("output is not valid Go: %v\n%s", err, src)
	}

	want := []string{
		"package camunda",
		"\t\"time\"\n",
		`camundaapi "` + clientImportPath + `"`,
		// Types become aliases, keeping their doc and linking to the full docs.
		"// ColourEnum is a widget colour.\n//\n// Fields and methods are documented on [camundaapi.ColourEnum].\ntype ColourEnum = camundaapi.ColourEnum\n",
		"type Widget = camundaapi.Widget\n",
		// A doc comment on a parenthesized type spec is carried over too.
		"// Shape is grouped in a parenthesized declaration, so its doc is on the spec.\n",
		"type Shape = camundaapi.Shape\n",
		// ...but a group's own comment is not copied onto an undocumented member.
		"\n\n// Fields and methods are documented on [camundaapi.Size].\ntype Size = camundaapi.Size\n",
		// Typed enum constants keep their literal value.
		"// List of ColourEnum\nconst (\n\tCOLOURENUM_RED ColourEnum = \"RED\"\n\tCOLOURENUM_BLUE ColourEnum = \"BLUE\"\n)",
		"AllowedColourEnumEnumValues = camundaapi.AllowedColourEnumEnumValues",
		// Functions become forwarding wrappers: variadic, unnamed params, no result.
		"// NewWidget instantiates a new Widget.\nfunc NewWidget(name string, tags ...string) *Widget {\n\treturn camundaapi.NewWidget(name, tags...)\n}",
		"func ParseWidget(p0 string, p1 bool) (Widget, error) {\n\treturn camundaapi.ParseWidget(p0, p1)\n}",
		"func Touch(w *Widget) {\n\tcamundaapi.Touch(w)\n}",
		"func NewNullableTime(val *time.Time) *NullableTime {\n\treturn camundaapi.NewNullableTime(val)\n}",
	}
	for _, w := range want {
		if !strings.Contains(src, w) {
			t.Errorf("re-export output missing %q\n--- generated ---\n%s", w, src)
		}
	}

	// Plumbing is excluded both by file (client.go) and by name (utils.go).
	for _, name := range []string{"APIClient", "Configuration", "NewConfiguration", "ContextAccessToken", "PtrString", "unexportedHelper", "contextKey"} {
		if strings.Contains(src, " "+name+" ") || strings.Contains(src, "func "+name+"(") {
			t.Errorf("%s must not be re-exported\n%s", name, src)
		}
	}

	const reexported = 16 // 5 in widget.go, 9 in model_widget.go, 2 in utils.go
	if count != reexported {
		t.Errorf("re-exported %d names, want %d", count, reexported)
	}
}

func TestGenerateReexportsRejectsNamesPackageCamundaDeclares(t *testing.T) {
	root := t.TempDir()
	write := func(name, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("clash.go", "package camunda\n\ntype Widget struct{}\n")
	write("clash_internal_test.go", "package camunda\n\nfunc Touch() {}\n")
	// Neither an external test package nor a file being regenerated can clash.
	write("clash_external_test.go", "package camunda_test\n\nfunc NewWidget() {}\n")
	write("reexport_generated.go", "package camunda\n\nfunc ParseWidget() {}\n")

	_, _, err := generateReexports("testdata/client", root, "reexport_generated.go")
	if err == nil {
		t.Fatal("expected a clash error")
	}
	for _, w := range []string{
		"Widget (declared in clash.go, re-exported from client/widget.go)",
		"Touch (declared in clash_internal_test.go, re-exported from client/model_widget.go)",
	} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error missing %q:\n%v", w, err)
		}
	}
	for _, name := range []string{"NewWidget", "ParseWidget"} {
		if strings.Contains(err.Error(), name) {
			t.Errorf("%s must not be reported as a clash:\n%v", name, err)
		}
	}
}
