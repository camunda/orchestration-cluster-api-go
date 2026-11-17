package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/exp/apidiff"
	"golang.org/x/tools/go/packages"
)

// excludedDirs are module-relative package trees that are not public API:
// examples/ holds README snippet sources, not code customers import.
var excludedDirs = []string{"examples"}

func goEnv() []string {
	return append(os.Environ(), "GOWORK=off")
}

// modulePath returns the path declared by the go.mod of the module in dir.
func modulePath(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-m")
	cmd.Dir = dir
	cmd.Env = goEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list -m in %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// compareWithRelease downloads modPath@version through the go command from proxy
// (the same proxy the version and go.mod were resolved from, so a baseline that
// resolves also downloads; GOSUMDB and GOFLAGS still apply) and returns the
// changes in dir that are incompatible with it.
func compareWithRelease(ctx context.Context, proxy, modPath, version, dir string) ([]string, error) {
	tmp, err := os.MkdirTemp("", "apicompat-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	cmd := exec.CommandContext(ctx, "go", "mod", "download", "-json", modPath+"@"+version)
	cmd.Dir = tmp
	cmd.Env = append(goEnv(), "GOPROXY="+proxy)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	var dl struct{ Dir, Error string }
	if err := json.Unmarshal(out, &dl); err != nil || dl.Error != "" || runErr != nil {
		return nil, fmt.Errorf("go mod download %s@%s: %s%s", modPath, version, dl.Error, strings.TrimSpace(stderr.String()))
	}

	// A copy outside the read-only module cache loads as an ordinary main module.
	oldDir := filepath.Join(tmp, "baseline")
	if err := copyTree(dl.Dir, oldDir); err != nil {
		return nil, err
	}
	return compareDirs(oldDir, dir)
}

// compareDirs returns the changes in the public API of the module in newDir that
// are incompatible with the module in oldDir, one apidiff message each.
func compareDirs(oldDir, newDir string) ([]string, error) {
	oldMod, err := loadModule(oldDir)
	if err != nil {
		return nil, fmt.Errorf("loading baseline: %w", err)
	}
	newMod, err := loadModule(newDir)
	if err != nil {
		return nil, fmt.Errorf("loading working tree: %w", err)
	}
	// apidiff prints conflicting-message diagnostics with fmt.Printf; keep them off the report.
	stdout := os.Stdout
	os.Stdout = os.Stderr
	report := apidiff.ModuleChanges(oldMod, newMod)
	os.Stdout = stdout

	var breaks []string
	for _, c := range report.Changes {
		if !c.Compatible {
			breaks = append(breaks, c.Message)
		}
	}
	sort.Strings(breaks)
	return breaks, nil
}

func loadModule(dir string) (*apidiff.Module, error) {
	cfg := &packages.Config{
		Dir:  dir,
		Env:  goEnv(),
		Fset: token.NewFileSet(),
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedModule,
	}
	loaded, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	var errs []error
	mod := &apidiff.Module{}
	for _, p := range loaded {
		for _, e := range p.Errors {
			errs = append(errs, e)
		}
		if p.Module == nil {
			errs = append(errs, fmt.Errorf("package %s is not in a module", p.PkgPath))
			continue
		}
		mod.Path = p.Module.Path
		if public(p, p.Module.Path) {
			mod.Packages = append(mod.Packages, p.Types)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if mod.Path == "" {
		return nil, fmt.Errorf("no packages in %s", dir)
	}
	return mod, nil
}

// public reports whether other modules can import p and it is part of the API.
func public(p *packages.Package, modPath string) bool {
	if p.Name == "main" {
		return false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(p.PkgPath, modPath), "/")
	for _, elem := range strings.Split(rel, "/") {
		if elem == "internal" {
			return false
		}
	}
	for _, d := range excludedDirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return false
		}
	}
	return true
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
