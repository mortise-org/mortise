/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolveSourceDir covers monorepo-subdirectory resolution for the build
// context, including path-traversal rejection.
func TestResolveSourceDir(t *testing.T) {
	// Build a minimal clone-like layout: <tmp>/services/api.
	clone := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clone, "services", "api"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// A file to test the "not-a-directory" branch.
	if err := os.WriteFile(filepath.Join(clone, "notadir"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	absPath := "/etc/passwd"
	if runtime.GOOS == "windows" {
		absPath = `C:\Windows`
	}

	tests := []struct {
		name       string
		path       string
		wantSuffix string // expected suffix of resolved dir; "" iff wantErr
		wantErr    string // substring we expect in the error
	}{
		{
			name:       "empty path returns clone root",
			path:       "",
			wantSuffix: "",
		},
		{
			name:       "valid subdirectory resolves",
			path:       "services/api",
			wantSuffix: filepath.Join("services", "api"),
		},
		{
			name:    "parent-directory traversal rejected",
			path:    "../etc/passwd",
			wantErr: "'..'",
		},
		{
			name:    "embedded parent segment rejected",
			path:    "services/../../etc",
			wantErr: "'..'",
		},
		{
			name:    "absolute path rejected",
			path:    absPath,
			wantErr: "must be relative",
		},
		{
			name:    "missing subdirectory errors clearly",
			path:    "services/nope",
			wantErr: "not found in repo",
		},
		{
			name:    "path that is a file errors clearly",
			path:    "notadir",
			wantErr: "not a directory",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSourceDir(clone, tc.path)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (got=%q)", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := clone
			if tc.wantSuffix != "" {
				want = filepath.Join(clone, tc.wantSuffix)
			}
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}
