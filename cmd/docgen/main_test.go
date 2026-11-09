package main

import "testing"

// TestDocumentKeepsOrphanFuncDocs covers a function whose result type is declared
// in an excluded file: go/doc drops it, and it must come back with its doc
// comment, not just its signature.
func TestDocumentKeepsOrphanFuncDocs(t *testing.T) {
	pkg, err := document("testdata/orphan", "example.com/orphan", "", "reexport.go")
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	for _, f := range pkg.Funcs {
		if f.Name == "NewRemote" {
			const want = "NewRemote returns a Remote. It is documented although Remote is excluded."
			if f.Doc != want {
				t.Errorf("NewRemote doc = %q, want %q", f.Doc, want)
			}
			return
		}
	}
	t.Fatalf("NewRemote missing from package funcs: %+v", pkg.Funcs)
}
