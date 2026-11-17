package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewestReleaseOrdersBySemverAndSkipsPrereleases(t *testing.T) {
	versions := []string{
		"v10.0.9", "v10.0.10", "v10.0.2",
		"v10.1.0-alpha.1", "v10.0.11-rc.1", // prereleases
		"v11.0.0", "v9.9.9", // other majors
		"v10.0.12+incompatible", "v10.1", "garbage", "", // not canonical releases
	}
	if got := newestRelease(versions, 10); got != "10.0.10" {
		t.Fatalf("newestRelease = %q, want 10.0.10", got)
	}
}

func TestNewestReleaseIsEmptyWithOnlyPrereleases(t *testing.T) {
	if got := newestRelease([]string{"v11.0.0-alpha.1", "v11.0.0-alpha.13", "v10.0.6"}, 11); got != "" {
		t.Fatalf("newestRelease = %q, want empty", got)
	}
}

func TestMajorModulePath(t *testing.T) {
	for _, tc := range []struct {
		in    string
		major int
		want  string
	}{
		{"example.com/sdk/v11", 10, "example.com/sdk/v10"},
		{"example.com/sdk/v10", 10, "example.com/sdk/v10"},
		{"example.com/sdk", 3, "example.com/sdk/v3"},
		{"example.com/sdk/v2", 1, "example.com/sdk"},
		{"example.com/sdk/v2", 0, "example.com/sdk"},
		{"example.com/v2sdk/v2", 4, "example.com/v2sdk/v4"},
	} {
		if got := majorModulePath(tc.in, tc.major); got != tc.want {
			t.Errorf("majorModulePath(%q, %d) = %q, want %q", tc.in, tc.major, got, tc.want)
		}
	}
}

func TestLatestReleaseQueriesTheEscapedModulePath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("v10.0.9\nv10.0.10\nv10.0.11-alpha.1\n"))
	}))
	defer srv.Close()

	got, err := latestRelease(context.Background(), srv.URL+"/", "example.com/Camunda/sdk/v10", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.0.10" {
		t.Errorf("latestRelease = %q, want 10.0.10", got)
	}
	if want := "/example.com/!camunda/sdk/v10/@v/list"; gotPath != want {
		t.Errorf("requested %q, want %q", gotPath, want)
	}
}

func TestLatestReleaseOfAnUnpublishedModuleIsEmpty(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusGone} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", code)
		}))
		got, err := latestRelease(context.Background(), srv.URL, "example.com/sdk/v12", 12)
		srv.Close()
		if err != nil || got != "" {
			t.Errorf("status %d: latestRelease = %q, %v; want empty, nil", code, got, err)
		}
	}
}

func TestLatestReleaseFailsOnAProxyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := latestRelease(context.Background(), srv.URL, "example.com/sdk/v10", 10); err == nil {
		t.Fatal("latestRelease succeeded on a 500; a proxy outage must not read as \"no baseline\"")
	}
}

func TestPublishedModulePathReadsTheServedGoMod(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("module example.com/Camunda/sdk/v10\n\ngo 1.26.0\n"))
	}))
	defer srv.Close()

	got, err := publishedModulePath(context.Background(), srv.URL, "example.com/Camunda/sdk/v10", "10.0.6")
	if err != nil {
		t.Fatal(err)
	}
	if got != "example.com/Camunda/sdk/v10" {
		t.Errorf("publishedModulePath = %q, want example.com/Camunda/sdk/v10", got)
	}
	if want := "/example.com/!camunda/sdk/v10/@v/v10.0.6.mod"; gotPath != want {
		t.Errorf("requested %q, want %q", gotPath, want)
	}
}

func TestPublishedModulePathFailsWithoutAModuleLine(t *testing.T) {
	for name, body := range map[string]string{
		"no module line": "go 1.26.0\n",
		"not a go.mod":   "boom",
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			if _, err := publishedModulePath(context.Background(), srv.URL, "example.com/sdk/v10", "10.0.6"); err == nil {
				t.Fatal("publishedModulePath succeeded on a go.mod with no module path")
			}
		})
	}
}

func TestPublishedModulePathFailsOnAProxyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := publishedModulePath(context.Background(), srv.URL, "example.com/sdk/v10", "10.0.6"); err == nil {
		t.Fatal("publishedModulePath succeeded on a 500; a proxy outage must not read as a baseline")
	}
}
