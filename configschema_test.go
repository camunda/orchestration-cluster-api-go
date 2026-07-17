package camunda

import (
	"os"
	"regexp"
	"testing"
)

// TestConfigSchemaMatchesReads is a drift guard between the canonical
// ConfigSchema registry (configschema.go) and the actual environment-variable
// reads in config.go. It fails if a CAMUNDA_*/ZEEBE_* variable is read but not
// registered, or registered but never read — keeping the documented
// configuration surface honest as new variables are added.
func TestConfigSchemaMatchesReads(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}

	// Every environment variable name referenced in config.go (reads and the
	// error messages that name them both use the same string literals).
	literalRe := regexp.MustCompile(`"((?:CAMUNDA|ZEEBE)_[A-Z0-9_]+)"`)
	inCode := map[string]bool{}
	for _, m := range literalRe.FindAllStringSubmatch(string(src), -1) {
		inCode[m[1]] = true
	}

	inSchema := map[string]bool{}
	for _, f := range ConfigSchema {
		if len(f.Keys) == 0 {
			t.Errorf("ConfigSchema entry with no keys: %+v", f)
		}
		for _, k := range f.Keys {
			if inSchema[k] {
				t.Errorf("ConfigSchema lists %q more than once", k)
			}
			inSchema[k] = true
		}
	}

	for k := range inCode {
		if !inSchema[k] {
			t.Errorf("env var %q is read in config.go but missing from ConfigSchema", k)
		}
	}
	for k := range inSchema {
		if !inCode[k] {
			t.Errorf("env var %q is in ConfigSchema but never read in config.go (stale entry)", k)
		}
	}
}
