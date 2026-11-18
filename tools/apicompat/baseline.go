package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

var majorSuffix = regexp.MustCompile(`/v[0-9]+$`)

// majorModulePath returns the module path that carries the given major: Go
// requires a /vN suffix from v2 on, and none below it.
func majorModulePath(modPath string, major int) string {
	base := majorSuffix.ReplaceAllString(modPath, "")
	if major < 2 {
		return base
	}
	return fmt.Sprintf("%s/v%d", base, major)
}

// latestRelease returns the newest non-prerelease version (without the leading
// "v") of the given major that the proxy lists for modPath, or "" if there is
// none. A stable branch is cut from main at an alpha, so the nearest git tag is
// not the baseline; the last release customers could have compiled against is.
func latestRelease(ctx context.Context, proxy, modPath string, major int) (string, error) {
	escaped, err := module.EscapePath(modPath)
	if err != nil {
		return "", err
	}
	url := strings.TrimSuffix(proxy, "/") + "/" + escaped + "/@v/list"
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("listing versions of %s: %w", modPath, err)
	}
	defer resp.Body.Close()
	// The proxy answers 404/410 for a module path nothing was ever published under.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("listing versions of %s: GET %s: %s", modPath, url, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("listing versions of %s: %w", modPath, err)
	}
	return newestRelease(strings.Fields(string(body)), major), nil
}

// publishedModulePath returns the module path declared by the go.mod the proxy
// serves for modPath@vversion — trusted metadata about what was actually
// published. Deriving the baseline path from the working tree instead would let
// a stable-branch change that renames the module path (and rewrites its imports)
// resolve an unpublished new path, find no baseline, and pass both gates while
// every existing importer breaks. The published path is the ground truth.
func publishedModulePath(ctx context.Context, proxy, modPath, version string) (string, error) {
	escaped, err := module.EscapePath(modPath)
	if err != nil {
		return "", err
	}
	url := strings.TrimSuffix(proxy, "/") + "/" + escaped + "/@v/v" + version + ".mod"
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s@v%s go.mod: %w", modPath, version, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s@v%s go.mod: GET %s: %s", modPath, version, url, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("fetching %s@v%s go.mod: %w", modPath, version, err)
	}
	path := modfile.ModulePath(body)
	if path == "" {
		return "", fmt.Errorf("%s@v%s go.mod declares no module path", modPath, version)
	}
	return path, nil
}

func newestRelease(versions []string, major int) string {
	want := fmt.Sprintf("v%d", major)
	best := ""
	for _, v := range versions {
		if semver.Canonical(v) != v || semver.Prerelease(v) != "" || semver.Major(v) != want {
			continue
		}
		if best == "" || semver.Compare(v, best) > 0 {
			best = v
		}
	}
	return strings.TrimPrefix(best, "v")
}
