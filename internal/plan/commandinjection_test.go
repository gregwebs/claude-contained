package plan

import (
	"reflect"
	"testing"
)

func TestParseCommandInjectionDefaultJSON(t *testing.T) {
	got, err := ParseCommandInjection([]byte(DefaultCommandInjectionJSON))
	if err != nil {
		t.Fatalf("ParseCommandInjection(default) error: %v", err)
	}
	want := map[string]string{"claude": "--add-dir", "codex": "--add-dir"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseCommandInjection(default) = %v, want %v", got, want)
	}
}

func TestParseCommandInjectionArbitraryProgram(t *testing.T) {
	got, err := ParseCommandInjection([]byte(`{"mytool":{"mountFlag":"--dir"}}`))
	if err != nil {
		t.Fatalf("ParseCommandInjection error: %v", err)
	}
	want := map[string]string{"mytool": "--dir"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseCommandInjection = %v, want %v", got, want)
	}
}

func TestParseCommandInjectionMalformedJSON(t *testing.T) {
	if _, err := ParseCommandInjection([]byte(`{ not json`)); err == nil {
		t.Error("ParseCommandInjection(malformed) = nil error, want error")
	}
}

func TestParseCommandInjectionEmptyMountFlag(t *testing.T) {
	if _, err := ParseCommandInjection([]byte(`{"claude":{"mountFlag":""}}`)); err == nil {
		t.Error("ParseCommandInjection(empty mountFlag) = nil error, want error")
	}
}

func TestParseCommandInjectionMissingMountFlag(t *testing.T) {
	if _, err := ParseCommandInjection([]byte(`{"claude":{}}`)); err == nil {
		t.Error("ParseCommandInjection(missing mountFlag) = nil error, want error")
	}
}

func TestParseCommandInjectionNonStringMountFlag(t *testing.T) {
	cases := []string{
		`{"claude":{"mountFlag":5}}`,
		`{"claude":{"mountFlag":true}}`,
		`{"claude":{"mountFlag":{}}}`,
	}
	for _, data := range cases {
		if _, err := ParseCommandInjection([]byte(data)); err == nil {
			t.Errorf("ParseCommandInjection(%s) = nil error, want error", data)
		}
	}
}

func TestParseCommandInjectionEmptyDocument(t *testing.T) {
	for _, data := range []string{`{}`, ``, `   `} {
		got, err := ParseCommandInjection([]byte(data))
		if err != nil {
			t.Fatalf("ParseCommandInjection(%q) error: %v", data, err)
		}
		if len(got) != 0 {
			t.Errorf("ParseCommandInjection(%q) = %v, want empty map", data, got)
		}
	}
}
