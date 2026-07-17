package camunda_test

import (
	"reflect"
	"testing"

	openapi "github.com/camunda/orchestration-cluster-api-go/client"
)

// TestResourceKeyIsValidatedStringKey guards the domain-type-system repair for
// semantic string keys the bundler metadata misses. ResourceKey is a scalar
// `type: string` path-parameter key, but its upstream schema omits the
// `x-semantic-type` extension, so it is absent from metadata.semanticKeys and
// openapi-generator emits it as a broken oneOf *struct*. hook_01 must repair it
// into a validated string newtype like every other key. This guards the defect
// class: a scalar key used as a path parameter must be a string newtype, not a
// struct.
func TestResourceKeyIsValidatedStringKey(t *testing.T) {
	// Underlying kind must be string (a struct here means the repair regressed).
	if k := reflect.TypeOf(openapi.ResourceKey("")).Kind(); k != reflect.String {
		t.Fatalf("ResourceKey underlying kind = %v, want string", k)
	}

	// Validated constructor accepts a well-formed key.
	key, err := openapi.NewResourceKey("2251799813685350")
	if err != nil {
		t.Fatalf("NewResourceKey rejected a valid key: %v", err)
	}
	if key.String() != "2251799813685350" {
		t.Errorf("ResourceKey.String() = %q, want 2251799813685350", key.String())
	}

	// Validated constructor rejects a malformed key.
	if _, err := openapi.NewResourceKey("not-a-key"); err == nil {
		t.Error("NewResourceKey accepted an invalid key; expected a validation error")
	}

	// Must* constructor is present and returns the key.
	if got := openapi.MustResourceKey("2251799813685350"); got.String() != "2251799813685350" {
		t.Errorf("MustResourceKey.String() = %q, want 2251799813685350", got.String())
	}
}
