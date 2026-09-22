package service

import (
	"opensync/internal/model"
	"opensync/internal/msg"
	"testing"
)

func TestSrcSelectionsNested(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  bool
	}{
		{"disjoint siblings", []string{"/data/a", "/data/b"}, false},
		{"same base name different parents", []string{"/a/docs", "/b/docs"}, false},
		{"parent and child", []string{"/data/x", "/data/x/y"}, true},
		{"child listed first", []string{"/data/x/y", "/data/x"}, true},
		{"deep descendant", []string{"/data/x", "/data/x/y/z/w"}, true},
		{"root swallows everything", []string{"/", "/data"}, true},
		{"single path", []string{"/data/x"}, false},
		{"trailing slash still nested", []string{"/data/x/", "/data/x/y/"}, true},
		{"sibling prefix is not nesting", []string{"/data/x", "/data/xy"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := srcSelectionsNested(tc.paths); got != tc.want {
				t.Fatalf("srcSelectionsNested(%v) = %v, want %v", tc.paths, got, tc.want)
			}
		})
	}
}

// ValidateJobInput must reject nested source selections: both of them resolve
// into the same destination subtree and would mutate it concurrently.
func TestValidateJobInputRejectsNestedSourcePaths(t *testing.T) {
	err := ValidateJobInput(map[string]interface{}{
		"srcPath": `["/data/x","/data/x/y"]`,
		"dstPath": `["/backup"]`,
		"alistId": 1,
		"method":  1,
		"isCron":  1,
	})
	if err == nil {
		t.Fatal("ValidateJobInput accepted nested source paths")
	}
	publicErr, ok := err.(model.PublicError)
	if !ok {
		t.Fatalf("error = %#v, want model.PublicError", err)
	}
	if string(publicErr) != msg.T(msg.SrcPathNested) {
		t.Fatalf("message = %q, want %q", string(publicErr), msg.T(msg.SrcPathNested))
	}
}

func TestValidateJobInputAllowsDisjointSourcePaths(t *testing.T) {
	if err := ValidateJobInput(map[string]interface{}{
		"srcPath": `["/data/a","/data/b"]`,
		"dstPath": `["/backup"]`,
		"alistId": 1,
		"method":  1,
		"isCron":  1,
	}); err != nil {
		t.Fatalf("ValidateJobInput() error: %v", err)
	}
}
