// Command changelog compares two versions of generated Go API client types
// and produces a structured changelog.
//
// Usage:
//
//	changelog --old pkg/camunda/8.8/client.gen.go --new pkg/camunda/8.9/client.gen.go
//	changelog --old-version 8.8 --new-version 8.9
//	changelog --all
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amanyadav/camunda-go-client/internal/changelog"
)

func main() {
	var (
		oldPath    string
		newPath    string
		oldVersion string
		newVersion string
		all        bool
		format     string
		output     string
		pkgDir     string
	)

	flag.StringVar(&oldPath, "old", "", "Path to old client.gen.go file")
	flag.StringVar(&newPath, "new", "", "Path to new client.gen.go file")
	flag.StringVar(&oldVersion, "old-version", "", "Old version label (e.g. 8.8). Resolves to pkg/camunda/<version>/client.gen.go")
	flag.StringVar(&newVersion, "new-version", "", "New version label (e.g. 8.9)")
	flag.BoolVar(&all, "all", false, "Compare all consecutive version pairs")
	flag.StringVar(&format, "format", "markdown", "Output format: markdown or json")
	flag.StringVar(&output, "output", "", "Output file path (default: stdout)")
	flag.StringVar(&pkgDir, "pkg-dir", "pkg/camunda", "Base directory for versioned packages")
	flag.Parse()

	if all {
		runAll(pkgDir, format, output)
		return
	}

	// Resolve paths from version labels if not given directly
	if oldPath == "" && oldVersion != "" {
		oldPath = filepath.Join(pkgDir, oldVersion, "client.gen.go")
	}
	if newPath == "" && newVersion != "" {
		newPath = filepath.Join(pkgDir, newVersion, "client.gen.go")
	}

	if oldPath == "" || newPath == "" {
		fmt.Fprintln(os.Stderr, "Error: provide --old/--new paths or --old-version/--new-version labels, or use --all")
		flag.Usage()
		os.Exit(1)
	}

	if oldVersion == "" {
		oldVersion = versionFromPath(oldPath)
	}
	if newVersion == "" {
		newVersion = versionFromPath(newPath)
	}

	result, err := runDiff(oldPath, newPath, oldVersion, newVersion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	out, err := formatResult(result, format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	writeOutput(out, output)

	// Exit with code 1 if there are breaking changes
	for _, c := range result.Changes {
		if c.Severity == changelog.SeverityBreaking {
			os.Exit(1)
		}
	}
}

func runDiff(oldPath, newPath, oldVersion, newVersion string) (*changelog.DiffResult, error) {
	fmt.Fprintf(os.Stderr, "Parsing %s...\n", oldPath)
	oldPkg, err := changelog.ParseFile(oldPath)
	if err != nil {
		return nil, fmt.Errorf("parsing old file: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Parsing %s...\n", newPath)
	newPkg, err := changelog.ParseFile(newPath)
	if err != nil {
		return nil, fmt.Errorf("parsing new file: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Old: %d structs, %d enums, %d aliases\n",
		len(oldPkg.Structs), len(oldPkg.Enums), len(oldPkg.Aliases))
	fmt.Fprintf(os.Stderr, "New: %d structs, %d enums, %d aliases\n",
		len(newPkg.Structs), len(newPkg.Enums), len(newPkg.Aliases))

	// Build role map from both packages (union)
	roles := changelog.BuildRoleMap(oldPkg)
	for k, v := range changelog.BuildRoleMap(newPkg) {
		if _, exists := roles[k]; !exists {
			roles[k] = v
		}
	}

	fmt.Fprintf(os.Stderr, "Diffing %s → %s...\n", oldVersion, newVersion)
	return changelog.Diff(oldPkg, newPkg, oldVersion, newVersion, roles), nil
}

func runAll(pkgDir, format, output string) {
	versions := discoverVersions(pkgDir)
	if len(versions) < 2 {
		fmt.Fprintln(os.Stderr, "Error: need at least 2 versions in", pkgDir)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Found versions: %s\n", strings.Join(versions, ", "))

	var allOutput strings.Builder
	hasBreaking := false

	for i := 0; i < len(versions)-1; i++ {
		oldV := versions[i]
		newV := versions[i+1]
		oldPath := filepath.Join(pkgDir, oldV, "client.gen.go")
		newPath := filepath.Join(pkgDir, newV, "client.gen.go")

		result, err := runDiff(oldPath, newPath, oldV, newV)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error comparing %s → %s: %v\n", oldV, newV, err)
			continue
		}

		out, err := formatResult(result, format)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting %s → %s: %v\n", oldV, newV, err)
			continue
		}

		allOutput.WriteString(out)
		allOutput.WriteString("\n")

		for _, c := range result.Changes {
			if c.Severity == changelog.SeverityBreaking {
				hasBreaking = true
			}
		}
	}

	writeOutput(allOutput.String(), output)

	if hasBreaking {
		os.Exit(1)
	}
}

func discoverVersions(pkgDir string) []string {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", pkgDir, err)
		os.Exit(1)
	}

	var versions []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Only include versioned directories (N.N pattern), skip "main"
		if strings.Contains(name, ".") {
			genPath := filepath.Join(pkgDir, name, "client.gen.go")
			if _, err := os.Stat(genPath); err == nil {
				versions = append(versions, name)
			}
		}
	}

	sort.Slice(versions, func(i, j int) bool {
		return versionLess(versions[i], versions[j])
	})

	return versions
}

func versionLess(a, b string) bool {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] != bParts[i] {
			// Simple numeric comparison via string (works for single digits)
			return aParts[i] < bParts[i]
		}
	}
	return len(aParts) < len(bParts)
}

func versionFromPath(p string) string {
	dir := filepath.Dir(p)
	return filepath.Base(dir)
}

func formatResult(r *changelog.DiffResult, format string) (string, error) {
	switch format {
	case "json":
		return changelog.GenerateJSON(r)
	default:
		return changelog.GenerateMarkdown(r), nil
	}
}

func writeOutput(content, outputPath string) {
	if outputPath != "" {
		dir := filepath.Dir(outputPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Report written to %s\n", outputPath)
	} else {
		fmt.Print(content)
	}
}
