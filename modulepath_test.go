package camunda_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// modulePathRef matches the module path, with any /vN major suffix, in files the
// compiler does not check. A preceding "https://" marks a repository URL instead.
var modulePathRef = regexp.MustCompile(`(https://)?github\.com/camunda/orchestration-cluster-api-go(/v[0-9]+)?\b`)

// skipDirs hold generated, vendored or ignored content; client/ and pb/ imports are
// compile-checked and their go_package comes from buf.gen.yaml, which is scanned.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "external-spec": true, "client": true,
	"pb": true, "dist": true, "docs-json": true, "docs-md": true,
}

// TestModulePathLiteralsMatchGoMod catches a /vN module path change that missed a
// literal the compiler cannot see: buf.gen.yaml, docs, scripts and comments.
func TestModulePathLiteralsMatchGoMod(t *testing.T) {
	base := "github.com/camunda/orchestration-cluster-api-go"
	if modulePath != base && !strings.HasPrefix(modulePath, base+"/v") {
		t.Fatalf("go.mod module %q is not %q or a /vN major of it", modulePath, base)
	}
	wantSuffix := strings.TrimPrefix(modulePath, base)

	scanned := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".md", ".yaml", ".yml", ".py", ".sh":
			if path == "modulepath_test.go" {
				return nil
			}
		default:
			if d.Name() != "Makefile" {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for i, line := range strings.Split(string(data), "\n") {
			for _, m := range modulePathRef.FindAllStringSubmatch(line, -1) {
				if m[1] == "" && m[2] != wantSuffix {
					t.Errorf("%s:%d: %q does not match go.mod module %q", path, i+1, m[0], modulePath)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("scanned no files; the walk is not looking at the repository")
	}
}
