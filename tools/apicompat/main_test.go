package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

// publish lays out files as version of testModPath in a GOPROXY directory.
func publish(t *testing.T, proxyDir, version string, files map[string]string) {
	t.Helper()
	src := writeModule(t, files)
	dir := filepath.Join(proxyDir, filepath.FromSlash(testModPath), "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	zf, err := os.Create(filepath.Join(dir, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.CreateFromDir(zf, module.Version{Path: testModPath, Version: version}, src); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		version + ".mod":  files["go.mod"],
		version + ".info": `{"Version":"` + version + `"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "list"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(version + "\n"); err != nil {
		t.Fatal(err)
	}
}

// fakeProxy serves v2.0.0, v2.1.0 (the baseline) and a newer prerelease of the
// test module, and points the go command at it with an isolated module cache.
func fakeProxy(t *testing.T) string {
	t.Helper()
	proxyDir := t.TempDir()
	publish(t, proxyDir, "v2.0.0", applyEdits(t, []edit{{"sdk.go", "", "\nfunc OnlyInOld() {}\n"}}))
	publish(t, proxyDir, "v2.1.0", baseModule)
	publish(t, proxyDir, "v2.2.0-alpha.1", applyEdits(t, []edit{{"sdk.go", "func Remove() {}\n", ""}}))
	srv := httptest.NewServer(http.FileServer(http.Dir(proxyDir)))
	t.Cleanup(srv.Close)

	modCache := t.TempDir()
	t.Setenv("GOPROXY", srv.URL)
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", modCache)
	t.Setenv("GOFLAGS", "-modcacherw")
	return srv.URL
}

func runApicompat(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	t.Logf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	return code, stdout.String()
}

func TestRunAgainstPublishedReleases(t *testing.T) {
	proxy := fakeProxy(t)
	compatible := writeModule(t, applyEdits(t, []edit{{"sdk.go", "", "\nfunc Added() {}\n"}}))
	breaking := writeModule(t, applyEdits(t, []edit{{"client/client.go", "Username string", "Username *string"}}))

	if code, out := runApicompat(t, "--repo", compatible, "--proxy", proxy, "--print-baseline-major", "2"); code != exitCompatible || out != "2.1.0\n" {
		t.Errorf("--print-baseline-major 2 = %d %q, want 0 \"2.1.0\\n\"", code, out)
	}
	if code, out := runApicompat(t, "--repo", compatible, "--proxy", proxy, "--print-baseline-major", "3"); code != exitCompatible || out != "" {
		t.Errorf("--print-baseline-major 3 = %d %q, want 0 and no output", code, out)
	}
	if code, out := runApicompat(t, "--repo", compatible, "--proxy", proxy, "--baseline-major", "3"); code != exitCompatible || !strings.Contains(out, "::notice::") {
		t.Errorf("--baseline-major 3 = %d %q, want 0 with a notice", code, out)
	}
	if code, _ := runApicompat(t, "--repo", compatible, "--proxy", proxy, "--baseline-major", "2"); code != exitCompatible {
		t.Errorf("compatible tree vs 2.1.0 exited %d, want 0", code)
	}
	code, out := runApicompat(t, "--repo", breaking, "--proxy", proxy, "--baseline-major", "2")
	if code != exitBreaking || !strings.Contains(out, "Username: changed from string to *string") {
		t.Errorf("breaking tree vs 2.1.0 = %d, want 1 listing the Username change", code)
	}
	// v2.0.0 had OnlyInOld, so the same compatible tree breaks against it.
	if code, _ := runApicompat(t, "--repo", compatible, "--baseline-version", "2.0.0"); code != exitBreaking {
		t.Errorf("compatible tree vs 2.0.0 exited %d, want 1", code)
	}
}

func TestRunSetupErrorsExitTwo(t *testing.T) {
	proxy := fakeProxy(t)
	repo := writeModule(t, baseModule)
	for name, args := range map[string][]string{
		"no mode":             {"--repo", repo},
		"two modes":           {"--repo", repo, "--baseline-major", "2", "--baseline-version", "2.1.0"},
		"unpublished version": {"--repo", repo, "--baseline-version", "2.9.9"},
		"prerelease baseline": {"--repo", repo, "--baseline-version", "2.2.0-alpha.1"},
		"not a module":        {"--repo", t.TempDir(), "--baseline-major", "2", "--proxy", proxy},
		"proxy down":          {"--repo", repo, "--baseline-major", "2", "--proxy", "http://127.0.0.1:1"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _ := runApicompat(t, args...); code != exitError {
				t.Errorf("exit %d, want 2", code)
			}
		})
	}
}
