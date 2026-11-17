// Command apicompat fails when the public API of the module in the working tree
// would stop code compiled against the last published release of a major from
// compiling. It is the stable-branch API compatibility guard; see AGENTS.md.
//
// Exit codes: 0 compatible (or no baseline published yet), 1 breaking changes
// found, 2 tool or setup error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	exitCompatible = 0
	exitBreaking   = 1
	exitError      = 2
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apicompat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "directory of the module to check")
	proxy := fs.String("proxy", "https://proxy.golang.org", "Go module proxy to resolve the baseline from")
	baselineMajor := fs.Int("baseline-major", -1, "compare against the newest published N.x.y release")
	baselineVersion := fs.String("baseline-version", "", "compare against this published release (X.Y.Z)")
	printMajor := fs.Int("print-baseline-major", -1, "print the newest published N.x.y release, or nothing if there is none")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	modes := 0
	for _, set := range []bool{*baselineMajor >= 0, *baselineVersion != "", *printMajor >= 0} {
		if set {
			modes++
		}
	}
	if modes != 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "apicompat: pass exactly one of --baseline-major, --baseline-version or --print-baseline-major")
		return exitError
	}

	modPath, err := modulePath(ctx, *repo)
	if err != nil {
		fmt.Fprintf(stderr, "apicompat: %v\n", err)
		return exitError
	}

	version := strings.TrimPrefix(*baselineVersion, "v")
	if major := max(*printMajor, *baselineMajor); major >= 0 {
		version, err = latestRelease(ctx, *proxy, majorModulePath(modPath, major), major)
		if err != nil {
			fmt.Fprintf(stderr, "apicompat: %v\n", err)
			return exitError
		}
		if *printMajor >= 0 {
			if version != "" {
				fmt.Fprintln(stdout, version)
			}
			return exitCompatible
		}
		if version == "" {
			fmt.Fprintf(stdout, "::notice::No %d.x release of %s is published yet; nothing to compare against.\n",
				major, majorModulePath(modPath, major))
			return exitCompatible
		}
	}
	if !semver.IsValid("v"+version) || semver.Prerelease("v"+version) != "" {
		fmt.Fprintf(stderr, "apicompat: baseline %q is not a release version X.Y.Z\n", version)
		return exitError
	}

	major, err := strconv.Atoi(strings.TrimPrefix(semver.Major("v"+version), "v"))
	if err != nil {
		fmt.Fprintf(stderr, "apicompat: baseline %q: %v\n", version, err)
		return exitError
	}
	baselinePath := majorModulePath(modPath, major)
	fmt.Fprintf(stdout, "Comparing the public API of %s (working tree) with %s@v%s\n", modPath, baselinePath, version)

	breaks, err := compareWithRelease(ctx, baselinePath, "v"+version, *repo)
	if err != nil {
		fmt.Fprintf(stderr, "apicompat: %v\n", err)
		return exitError
	}
	if len(breaks) == 0 {
		fmt.Fprintln(stdout, "No breaking API changes.")
		return exitCompatible
	}
	fmt.Fprintf(stdout, "Breaking API changes (%d):\n", len(breaks))
	for _, b := range breaks {
		fmt.Fprintf(stdout, "- %s\n", b)
	}
	return exitBreaking
}
