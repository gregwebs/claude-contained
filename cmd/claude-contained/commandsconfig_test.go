package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadCommandInjectionAbsentFileReturnsEmptyMap(t *testing.T) {
	project := t.TempDir()

	injection, present, err := readCommandInjection(project)
	if err != nil {
		t.Fatalf("readCommandInjection: %v", err)
	}
	if present {
		t.Error("present = true, want false for an absent file")
	}
	if len(injection) != 0 {
		t.Errorf("injection = %v, want empty", injection)
	}
}

func TestReadCommandInjectionPresentValidFile(t *testing.T) {
	project := t.TempDir()
	writeCommandsJSON(t, project, `{"mytool":{"mountFlag":"--dir"}}`)

	injection, present, err := readCommandInjection(project)
	if err != nil {
		t.Fatalf("readCommandInjection: %v", err)
	}
	if !present {
		t.Error("present = false, want true")
	}
	want := map[string]string{"mytool": "--dir"}
	if !reflect.DeepEqual(injection, want) {
		t.Errorf("injection = %v, want %v", injection, want)
	}
}

func TestReadCommandInjectionMalformedFilePropagatesError(t *testing.T) {
	project := t.TempDir()
	path := writeCommandsJSON(t, project, `{ not json`)

	_, _, err := readCommandInjection(project)
	if err == nil {
		t.Fatal("readCommandInjection: got nil error, want one naming the path")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the path %q", err.Error(), path)
	}
}

// writeCommandsJSON writes <projectDir>/.claude-contained/commands.json and
// returns its path.
func writeCommandsJSON(t *testing.T, projectDir, content string) string {
	t.Helper()
	path := commandInjectionConfigPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
