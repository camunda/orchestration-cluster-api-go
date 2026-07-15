package camunda

import (
	"errors"
	"fmt"
	"testing"
)

func TestAPIErrorMessage(t *testing.T) {
	e := &APIError{Status: 404, Body: `{"detail":"not found"}`}
	if got := e.Error(); got == "" || !contains(got, "404") || !contains(got, "not found") {
		t.Errorf("unexpected message: %q", got)
	}
	bare := &APIError{Status: 500}
	if got := bare.Error(); !contains(got, "500") {
		t.Errorf("unexpected bare message: %q", got)
	}
}

func TestStatusCodeUnwraps(t *testing.T) {
	wrapped := fmt.Errorf("call failed: %w", &APIError{Status: 409, Body: "conflict"})
	status, ok := StatusCode(wrapped)
	if !ok || status != 409 {
		t.Errorf("StatusCode = (%d, %v), want (409, true)", status, ok)
	}
	if _, ok := StatusCode(errors.New("plain")); ok {
		t.Error("StatusCode should report ok=false for a non-API error")
	}
}

func TestSentinelWrapping(t *testing.T) {
	err := configErrorf("bad %s", "value")
	if !errors.Is(err, ErrConfig) {
		t.Error("configErrorf should wrap ErrConfig")
	}
	aerr := authErrorf("token %d", 1)
	if !errors.Is(aerr, ErrAuth) {
		t.Error("authErrorf should wrap ErrAuth")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
