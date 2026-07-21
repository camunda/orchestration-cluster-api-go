package camunda_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/camunda/orchestration-cluster-api-go"

// leafPackages are internal runtime packages that must not import any other
// internal package.
var leafPackages = map[string]bool{
	"diag":         true,
	"auth":         true,
	"retry":        true,
	"backpressure": true,
	"falcon":       true,
}

// composerAllowed maps composer packages to the internal packages they may import.
var composerAllowed = map[string]map[string]bool{
	"transport": {"auth": true, "retry": true, "backpressure": true, "diag": true},
}

// internalImports returns the import paths of the non-test Go files in pkgDir.
func internalImports(t *testing.T, pkgDir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var imports []string
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("read %s: %v", pkgDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(pkgDir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			imports = append(imports, p)
		}
	}
	return imports
}

func internalPackageDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("internal")
	if err != nil {
		t.Fatalf("read internal/: %v", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs
}

// TestRuntimeDoesNotImportGeneratedCode guards the deliberate independence of the
// hand-written runtime from the generated client/ and pb/ packages, and forbids
// upward imports of the root package.
func TestRuntimeDoesNotImportGeneratedCode(t *testing.T) {
	for _, name := range internalPackageDirs(t) {
		for _, imp := range internalImports(t, filepath.Join("internal", name)) {
			switch {
			case strings.HasPrefix(imp, modulePath+"/client"), strings.HasPrefix(imp, modulePath+"/pb"):
				t.Errorf("internal/%s imports generated code %q; the runtime must stay independent of client/ and pb/", name, imp)
			case imp == modulePath:
				t.Errorf("internal/%s imports the root package; internal packages must not depend upward", name)
			}
		}
	}
}

// TestInternalLayering enforces that leaf packages import no other internal
// package and composer packages import only their declared dependencies.
func TestInternalLayering(t *testing.T) {
	prefix := modulePath + "/internal/"
	for _, name := range internalPackageDirs(t) {
		var deps []string
		for _, imp := range internalImports(t, filepath.Join("internal", name)) {
			if strings.HasPrefix(imp, prefix) {
				deps = append(deps, strings.TrimPrefix(imp, prefix))
			}
		}
		switch {
		case leafPackages[name]:
			if len(deps) > 0 {
				t.Errorf("leaf package internal/%s must not import other internal packages, but imports %v", name, deps)
			}
		case composerAllowed[name] != nil:
			for _, dep := range deps {
				if !composerAllowed[name][dep] {
					t.Errorf("internal/%s imports internal/%s, which is not in its allowed set", name, dep)
				}
			}
		default:
			if len(deps) > 0 {
				t.Errorf("internal/%s is unclassified in the layering map but imports %v; update architecture_test.go", name, deps)
			}
		}
	}
}
