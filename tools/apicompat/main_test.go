package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	if code, _ := runApicompat(t, "--repo", compatible, "--proxy", proxy, "--baseline-version", "2.0.0"); code != exitBreaking {
		t.Errorf("compatible tree vs 2.0.0 exited %d, want 1", code)
	}
}

// A stable-branch change that renames the module path (and rewrites its imports)
// must not slip past the gate by resolving an unpublished new path with no
// baseline. The baseline path comes from the published release's own go.mod, so
// the rename is reported as the break it is.
func TestRunRenamedModulePathIsBreaking(t *testing.T) {
	proxy := fakeProxy(t)
	// Same code as the published v2.1.0, but the go.mod declares a new path.
	renamed := writeModule(t, applyEdits(t, []edit{
		{"go.mod", "module " + testModPath, "module example.com/sdk/v3"},
		{"sdk.go", `import "example.com/sdk/v2/client"`, `import "example.com/sdk/v3/client"`},
	}))
	code, out := runApicompat(t, "--repo", renamed, "--proxy", proxy, "--baseline-version", "2.1.0")
	if code != exitBreaking || !strings.Contains(out, "module path changed from "+testModPath+" to example.com/sdk/v3") {
		t.Errorf("renamed module = %d %q, want 1 reporting the path change", code, out)
	}
	// The --baseline-major path resolves the version through the renamed tree's
	// candidate (/v2) and must still catch the rename against the published go.mod.
	code, out = runApicompat(t, "--repo", renamed, "--proxy", proxy, "--baseline-major", "2")
	if code != exitBreaking || !strings.Contains(out, "module path changed from "+testModPath+" to example.com/sdk/v3") {
		t.Errorf("renamed module via --baseline-major = %d %q, want 1 reporting the path change", code, out)
	}
}

func TestRunSetupErrorsExitTwo(t *testing.T) {
	proxy := fakeProxy(t)
	repo := writeModule(t, baseModule)
	for name, args := range map[string][]string{
		"no mode":             {"--repo", repo},
		"two modes":           {"--repo", repo, "--baseline-major", "2", "--baseline-version", "2.1.0"},
		"unpublished version": {"--repo", repo, "--baseline-version", "2.9.9", "--proxy", proxy},
		"prerelease baseline": {"--repo", repo, "--baseline-version", "2.2.0-alpha.1", "--proxy", proxy},
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

type workflowFixture struct {
	checkerCode      int
	reportError      bool
	summaryError     bool
	approved         bool
	dispatchApproved bool
	prs              string
	gitTags          string // newline-separated tags `git tag --list` returns
	noBaseline       bool   // the proxy lists no release for the major
}

func runWorkflowCheck(t *testing.T, workflow string, fixture workflowFixture) int {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", workflow))
	if err != nil {
		t.Fatal(err)
	}
	_, step, found := strings.Cut(string(content), "- name: Check API compatibility\n")
	if !found {
		t.Fatal("missing compatibility step")
	}
	_, block, found := strings.Cut(step, "        run: |\n")
	if !found {
		t.Fatal("missing compatibility shell block")
	}
	var script strings.Builder
	for _, line := range strings.Split(block, "\n") {
		if line != "" && !strings.HasPrefix(line, "          ") {
			break
		}
		script.WriteString(strings.TrimPrefix(line, "          ") + "\n")
	}
	temp := t.TempDir()
	for name, body := range map[string]string{
		"go": "exit 0\n",
		"apicompat": `if [ "$1" = "--print-baseline-major" ]; then
  [ "$NO_BASELINE" = "true" ] || printf '10.0.6\n'
  exit 0
fi
printf 'fixture report\n'
exit "$CHECKER_CODE"
`,
		"git": `case "$1" in
  rev-parse) exit 0 ;;
  log) printf 'checked-commit\n' ;;
  tag) printf '%s\n' "$GIT_TAGS" | grep . | sort -Vr ;;
  *) exit 2 ;;
esac
`,
		"gh": "printf '%s\\n' \"$PR_RECORDS\"\n",
	} {
		if err := os.WriteFile(filepath.Join(temp, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if fixture.reportError {
		if err := os.Mkdir(filepath.Join(temp, "apicompat.txt"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	summary := filepath.Join(temp, "summary.txt")
	if fixture.summaryError {
		summary = temp
	}
	command := exec.Command("bash", "-e", "-s")
	command.Stdin = strings.NewReader(script.String())
	command.Env = append(os.Environ(),
		"PATH="+temp+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RUNNER_TEMP="+temp,
		"GITHUB_STEP_SUMMARY="+summary,
		"BASE_REF=stable/10",
		"GITHUB_REF_NAME=stable/10",
		"GITHUB_REPOSITORY=fixture/sdk",
		fmt.Sprintf("CHECKER_CODE=%d", fixture.checkerCode),
		fmt.Sprintf("APPROVED=%t", fixture.approved),
		fmt.Sprintf("DISPATCH_APPROVED=%t", fixture.dispatchApproved),
		"PR_RECORDS="+fixture.prs,
		"GIT_TAGS="+fixture.gitTags,
		fmt.Sprintf("NO_BASELINE=%t", fixture.noBaseline),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatal(err)
		}
	}
	t.Logf("%s exit %d: %s", workflow, command.ProcessState.ExitCode(), output)
	return command.ProcessState.ExitCode()
}

func TestWorkflowReportErrorsCannotBeApproved(t *testing.T) {
	for _, workflow := range []string{"api-compat.yml", "release.yml"} {
		for _, checkerCode := range []int{0, 1, 2} {
			for _, failure := range []string{"report", "summary"} {
				t.Run(fmt.Sprintf("%s/%s/exit%d", workflow, failure, checkerCode), func(t *testing.T) {
					code := runWorkflowCheck(t, workflow, workflowFixture{
						checkerCode:      checkerCode,
						reportError:      failure == "report",
						summaryError:     failure == "summary",
						approved:         true,
						dispatchApproved: true,
					})
					if code != exitError {
						t.Errorf("report failure exited %d, want setup error 2", code)
					}
				})
			}
		}
	}
}

func TestWorkflowApprovalExitCodes(t *testing.T) {
	for _, workflow := range []string{"api-compat.yml", "release.yml"} {
		for _, checkerCode := range []int{0, 1, 2} {
			for _, approved := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/exit%d/approved%t", workflow, checkerCode, approved), func(t *testing.T) {
					code := runWorkflowCheck(t, workflow, workflowFixture{
						checkerCode: checkerCode, approved: approved, dispatchApproved: approved,
					})
					want := checkerCode
					if checkerCode == exitBreaking && approved {
						want = exitCompatible
					}
					if code != want {
						t.Errorf("exit %d, want %d", code, want)
					}
				})
			}
		}
	}
}

func TestReleaseApprovalRequiresTargetBranchAndCheckedMerge(t *testing.T) {
	for _, scenario := range []struct {
		name string
		prs  string
		want int
	}{
		{"none", "", exitBreaking},
		{"main", "123\tmain\tchecked-commit", exitBreaking},
		{"other stable", "123\tstable/9\tchecked-commit", exitBreaking},
		{"outside range", "123\tstable/10\tolder-commit", exitBreaking},
		{"missing merge", "123\tstable/10\t", exitBreaking},
		{"target merge", "123\tstable/10\tchecked-commit", exitCompatible},
		{"mixed", "123\tmain\tchecked-commit\n124\tstable/10\tchecked-commit", exitCompatible},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			code := runWorkflowCheck(t, "release.yml", workflowFixture{checkerCode: exitBreaking, prs: scenario.prs})
			if code != scenario.want {
				t.Errorf("exit %d, want %d", code, scenario.want)
			}
		})
	}
}

// The proxy version list can lag a just-pushed release tag (golang/go#75191). When
// git already holds a newer stable tag for the major than the proxy returned, the
// baseline is stale and the gate must fail closed (setup error), not compare against
// an outdated release and pass a break to the real latest.
func TestReleaseFailsClosedOnAStaleProxyBaseline(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		gitTags string
		want    int
	}{
		// The stub checker resolves the proxy baseline to 10.0.6 (tag v10.0.6).
		{"proxy current", "v10.0.6", exitBreaking},
		{"git behind proxy", "v10.0.5", exitBreaking},
		{"no stable tags", "", exitBreaking},
		{"prerelease only newer", "v10.0.6\nv10.1.0-alpha.1", exitBreaking},
		{"stale proxy", "v10.0.6\nv10.0.7", exitError},
		{"stale proxy minor", "v10.0.6\nv10.1.0", exitError},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			code := runWorkflowCheck(t, "release.yml", workflowFixture{
				checkerCode: exitBreaking, // would pass approval if reached; the stale guard must fire first
				approved:    true,
				gitTags:     scenario.gitTags,
			})
			if code != scenario.want {
				t.Errorf("exit %d, want %d", code, scenario.want)
			}
		})
	}
}

// An empty proxy list (or cached 404) is only "nothing released yet" when git holds
// no stable tag for the major either; otherwise the proxy lags and the release gate
// must fail closed rather than skip the comparison.
func TestReleaseFailsClosedWhenProxyListsNoReleaseButGitHasOne(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		gitTags string
		want    int
	}{
		{"nothing released", "", 0},
		{"only prereleases", "v10.0.0-alpha.1", 0},
		{"stable tag not yet indexed", "v10.0.0", exitError},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			code := runWorkflowCheck(t, "release.yml", workflowFixture{
				checkerCode: exitBreaking,
				noBaseline:  true,
				gitTags:     scenario.gitTags,
			})
			if code != scenario.want {
				t.Errorf("exit %d, want %d", code, scenario.want)
			}
		})
	}
}
