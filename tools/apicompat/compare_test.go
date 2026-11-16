package main

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testModPath = "example.com/sdk/v2"

// baseModule is a small module shaped like the SDK: a root package that
// re-exports client types through aliases, plus packages that are not public API.
var baseModule = map[string]string{
	"go.mod": "module " + testModPath + "\n\ngo 1.26\n",
	"sdk.go": `package sdk

import "example.com/sdk/v2/client"

type Config struct {
	Name string
	Port int
}

type Worker interface {
	Run() error
}

type Client struct{}

func (c *Client) Do(id string) error { return nil }

func New(cfg Config) *Client { return &Client{} }

func Remove() {}

type User = client.User

const Mode = "a"
`,
	"client/client.go": `package client

type NullableString struct{ value *string }

type User struct {
	Username string
}
`,
	"extra/extra.go":      "package extra\n\nfunc Extra() {}\n",
	"internal/x/x.go":     "package x\n\nfunc Helper(a int) {}\n",
	"cmd/tool/main.go":    "package main\n\nfunc Exported(a int) {}\n\nfunc main() {}\n",
	"examples/example.go": "package examples\n\nfunc Snippet(a int) {}\n",
}

type edit struct {
	file, old, new string // old == "" appends; new == "" with old == "*" deletes the file
}

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func applyEdits(t *testing.T, edits []edit) map[string]string {
	t.Helper()
	files := maps.Clone(baseModule)
	for _, e := range edits {
		switch e.old {
		case "*":
			delete(files, e.file)
		case "":
			files[e.file] += e.new
		default:
			if !strings.Contains(files[e.file], e.old) {
				t.Fatalf("%s does not contain %q", e.file, e.old)
			}
			files[e.file] = strings.Replace(files[e.file], e.old, e.new, 1)
		}
	}
	return files
}

func TestCompareIdenticalModulesIsCompatible(t *testing.T) {
	breaks, err := compareDirs(writeModule(t, baseModule), writeModule(t, baseModule))
	if err != nil {
		t.Fatal(err)
	}
	if len(breaks) != 0 {
		t.Fatalf("identical modules reported breaks: %q", breaks)
	}
}

func TestCompareReportsEveryBreakClass(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edits []edit
		want  string // substring of the reported break
	}{
		{"removed func", []edit{{"sdk.go", "func Remove() {}\n", "\n"}}, "Remove: removed"},
		{"removed method", []edit{{"sdk.go", "func (c *Client) Do(id string) error { return nil }\n", "\n"}}, "Do: removed"},
		{"removed field", []edit{{"sdk.go", "\tPort int\n", ""}}, "Port: removed"},
		{"field T to *T", []edit{{"client/client.go", "Username string", "Username *string"}}, "Username: changed from string to *string"},
		{"field T to Nullable wrapper", []edit{{"client/client.go", "Username string", "Username NullableString"}}, "Username: changed from string to"},
		{"field type change seen through an alias", []edit{{"client/client.go", "Username string", "Username int"}}, "Username"},
		{"new method on exported interface", []edit{{"sdk.go", "\tRun() error\n", "\tRun() error\n\tStop()\n"}}, "Worker.Stop: added"},
		{"changed parameter type", []edit{{"sdk.go", "Do(id string)", "Do(id int)"}}, "Do: changed from func(string) error to func(int) error"},
		{"added parameter", []edit{{"sdk.go", "func New(cfg Config)", "func New(cfg Config, extra int)"}}, "New: changed from"},
		{"changed result", []edit{{"sdk.go", "func New(cfg Config) *Client { return &Client{} }", "func New(cfg Config) Client { return Client{} }"}}, "New: changed from"},
		{"changed constant value", []edit{{"sdk.go", `Mode = "a"`, `Mode = "b"`}}, "Mode: value changed"},
		{"removed package", []edit{{"extra/extra.go", "*", ""}}, "extra: removed"},
		{"removed alias", []edit{{"sdk.go", "type User = client.User\n", "var _ client.User\n"}}, "User: removed"},
		{"alias retargeted", []edit{{"sdk.go", "type User = client.User\n", "type User = client.NullableString\n"}}, "User: changed from"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			breaks, err := compareDirs(writeModule(t, baseModule), writeModule(t, applyEdits(t, tc.edits)))
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range breaks {
				if strings.Contains(b, tc.want) {
					return
				}
			}
			t.Fatalf("no break containing %q; got %q", tc.want, breaks)
		})
	}
}

func TestCompareAcceptsCompatibleChanges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edits []edit
	}{
		{"added func", []edit{{"sdk.go", "", "\nfunc Added() {}\n"}}},
		{"added struct field", []edit{{"sdk.go", "\tPort int\n", "\tPort int\n\tHost string\n"}}},
		{"added method on struct", []edit{{"sdk.go", "", "\nfunc (c *Client) Undo() {}\n"}}},
		{"added package", []edit{{"more/more.go", "", "package more\n\nfunc More() {}\n"}}},
		{"field added to an aliased type", []edit{{"client/client.go", "\tUsername string\n", "\tUsername string\n\tEmail *string\n"}}},
		{"internal package changed", []edit{{"internal/x/x.go", "Helper(a int)", "Helper(a string)"}}},
		{"main package changed", []edit{{"cmd/tool/main.go", "Exported(a int)", "Exported(a string)"}}},
		{"examples package changed", []edit{{"examples/example.go", "Snippet(a int)", "Snippet(a string)"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			breaks, err := compareDirs(writeModule(t, baseModule), writeModule(t, applyEdits(t, tc.edits)))
			if err != nil {
				t.Fatal(err)
			}
			if len(breaks) != 0 {
				t.Fatalf("compatible change reported as breaking: %q", breaks)
			}
		})
	}
}

func TestCompareFailsWhenTheWorkingTreeDoesNotBuild(t *testing.T) {
	broken := applyEdits(t, []edit{{"sdk.go", "", "\nfunc Broken() { undefined() }\n"}})
	if _, err := compareDirs(writeModule(t, baseModule), writeModule(t, broken)); err == nil {
		t.Fatal("compareDirs succeeded on a module that does not type-check")
	}
}
