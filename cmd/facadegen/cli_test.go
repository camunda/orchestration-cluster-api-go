package main

import "testing"

func TestRequireMetadataPathRejectsOmittedMetadata(t *testing.T) {
	if err := requireMetadataPath(""); err == nil {
		t.Fatal("expected error when metadata path is omitted")
	}
	if err := requireMetadataPath("external-spec/bundled/spec-metadata.json"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
