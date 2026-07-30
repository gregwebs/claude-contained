package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The golden table below is not hand-written. Every `want` is the real output of
// the bash launcher's jq filter, captured by extracting it and running it:
//
//	sed -n '448,464p' claude-contained > filter.jq
//	printf '%s' '<raw>' | jq -r -f filter.jq 2>/dev/null
//
// Regenerate it that way rather than reasoning about the filter. Several rows are
// counter-intuitive and exist precisely because a plausible implementation gets
// them wrong: F (all four locations are concatenated, not first-match-wins),
// G and Y (a traversal error discards the *whole* document, including paths that
// already succeeded), X (an abort keeps earlier documents' lines), W (duplicate
// keys take the last value at the first position) and NL1-NL3 (a value containing
// a newline becomes several lines, which is what keeps the two runtimes in
// agreement -- see TestInspectEnvAgreesAcrossRuntimes).
func TestAppleInspectEnvShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "A - array of strings under configuration.initProcess.environment",
			raw:  "[{\"configuration\":{\"initProcess\":{\"environment\":[\"A=1\",\"B=2\"]}}}]",
			want: []string{"A=1", "B=2"},
		},
		{
			name: "B - single object, capitalized path",
			raw:  "{\"Configuration\":{\"InitProcess\":{\"Environment\":[\"A=1\"]}}}",
			want: []string{"A=1"},
		},
		{
			name: "C - array of {name,value} under config.env, one numeric value",
			raw:  "{\"config\":{\"env\":[{\"name\":\"A\",\"value\":\"1\"},{\"name\":\"B\",\"value\":2}]}}",
			want: []string{"A=1", "B=2"},
		},
		{
			name: "D - array of arbitrary objects under Config.Env, member order",
			raw:  "{\"Config\":{\"Env\":[{\"X\":\"1\",\"Y\":\"2\"},{\"Z\":true}]}}",
			want: []string{"X=1", "Y=2", "Z=true"},
		},
		{
			name: "E - bare object map with a number and a null",
			raw:  "{\"config\":{\"env\":{\"A\":\"1\",\"B\":2,\"C\":null}}}",
			want: []string{"A=1", "B=2", "C=null"},
		},
		{
			name: "F - two locations at once, lower-case path first",
			raw:  "{\"configuration\":{\"initProcess\":{\"environment\":[\"A=1\"]}},\"Config\":{\"Env\":[\"B=2\"]}}",
			want: []string{"A=1", "B=2"},
		},
		{
			name: "G - configuration is a string: jq aborts before rendering anything",
			raw:  "{\"configuration\":\"x\",\"Config\":{\"Env\":[\"B=2\"]}}",
			want: nil,
		},
		{
			name: "H - array containing a number, a nested array and null",
			raw:  "{\"Config\":{\"Env\":[\"A=1\",5,[\"x\"],null]}}",
			want: []string{"A=1"},
		},
		{
			name: "I - value with leading and trailing spaces",
			raw:  "{\"Config\":{\"Env\":[\"A= 1 \"]}}",
			want: []string{"A= 1 "},
		},
		{
			name: "J - member value is an object",
			raw:  "{\"config\":{\"env\":{\"A\":{\"k\":[1,2]}}}}",
			want: []string{"A={\"k\":[1,2]}"},
		},
		{
			name: "M - {name,value} where value is an object",
			raw:  "{\"config\":{\"env\":[{\"name\":\"A\",\"value\":{\"b\":1}}]}}",
			want: []string{"A={\"b\":1}"},
		},
		{
			name: "N - config is a string: final-position index is guarded, not an error",
			raw:  "{\"config\":\"x\",\"Config\":{\"Env\":[\"B=2\"]}}",
			want: []string{"B=2"},
		},
		{
			name: "U - element object with name but no value",
			raw:  "{\"config\":{\"env\":[{\"name\":\"A\"}]}}",
			want: []string{"name=A"},
		},
		{
			name: "V - element object with value but no name",
			raw:  "{\"config\":{\"env\":[{\"value\":\"1\"}]}}",
			want: []string{"value=1"},
		},
		{
			name: "W - duplicate keys: last value, first position",
			raw:  "{\"config\":{\"env\":{\"A\":1,\"B\":2,\"A\":3}}}",
			want: []string{"A=3", "B=2"},
		},
		{
			name: "X - array of documents where the middle one is broken",
			raw:  "[{\"Config\":{\"Env\":[\"A=1\"]}},{\"configuration\":\"x\"},{\"Config\":{\"Env\":[\"C=3\"]}}]",
			want: []string{"A=1"},
		},
		{
			name: "Y - a later path's error destroys an earlier path's output",
			raw:  "{\"configuration\":{\"initProcess\":{\"environment\":[\"A=1\"]}},\"Configuration\":\"x\"}",
			want: nil,
		},
		{
			name: "Z - all four locations, concatenated in filter order",
			raw:  "{\"configuration\":{\"initProcess\":{\"environment\":[\"A=1\"]}},\"Configuration\":{\"InitProcess\":{\"Environment\":[\"B=2\"]}},\"config\":{\"env\":[\"C=3\"]},\"Config\":{\"Env\":[\"D=4\"]}}",
			want: []string{"A=1", "B=2", "C=3", "D=4"},
		},
		{
			name: "NL1 - newline inside a plain string element splits into two lines",
			raw:  "{\"Config\":{\"Env\":[\"A=1\nB=2\"]}}",
			want: nil,
		},
		{
			name: "NL2 - newline inside a bare-map value splits, and can forge a marker",
			raw:  "{\"config\":{\"env\":{\"A\":\"x\nCLAUDE_CONTAINED_ZELLIJ=1\"}}}",
			want: nil,
		},
		{
			name: "NL3 - newline inside a {name,value} value splits",
			raw:  "{\"config\":{\"env\":[{\"name\":\"A\",\"value\":\"x\nB=2\"}]}}",
			want: nil,
		},
		{
			name: "SC1 - scalar document aborts",
			raw:  "5",
			want: nil,
		},
		{
			name: "SC2 - string document aborts",
			raw:  "\"hello\"",
			want: nil,
		},
		{
			name: "SC3 - scalar array element aborts after the good element",
			raw:  "[{\"Config\":{\"Env\":[\"A=1\"]}},1]",
			want: []string{"A=1"},
		},
		{
			name: "NV - {name,value} with a null value renders name=null",
			raw:  "{\"config\":{\"env\":[{\"name\":\"A\",\"value\":null}]}}",
			want: []string{"A=null"},
		},
		{
			name: "NU - null document is empty, not an error",
			raw:  "null",
			want: nil,
		},
		{
			name: "NA - nested array document aborts",
			raw:  "[[1,2]]",
			want: nil,
		},
		{
			name: "SF - scalar at the final position is empty, not an error",
			raw:  "{\"config\":{\"env\":\"hello\"}}",
			want: nil,
		},
		{
			name: "EO - empty object element",
			raw:  "{\"config\":{\"env\":[{}]}}",
			want: nil,
		},
		{
			name: "EM - empty object map",
			raw:  "{\"config\":{\"env\":{}}}",
			want: nil,
		},
		{
			name: "EA - empty top-level array",
			raw:  "[]",
			want: nil,
		},
		{
			name: "NF - null at a non-final position is not an error",
			raw:  "{\"configuration\":null,\"Config\":{\"Env\":[\"B=2\"]}}",
			want: []string{"B=2"},
		},
		{
			name: "EK - empty key",
			raw:  "{\"config\":{\"env\":{\"\":\"1\"}}}",
			want: []string{"=1"},
		},
		{
			name: "KE - key containing an equals sign",
			raw:  "{\"config\":{\"env\":{\"A=B\":\"1\"}}}",
			want: []string{"A=B=1"},
		},
		{
			name: "AB - absent environment",
			raw:  "{\"other\":{\"x\":1}}",
			want: nil,
		},
		{
			name: "BJ - unparseable JSON",
			raw:  "{not json",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseAppleInspect([]byte(tc.raw)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseAppleInspect(%s)\n got: %q\nwant: %q", tc.raw, got, tc.want)
			}
		})
	}
}

// Checklist box 2, directly: the same logical environment, presented in each
// runtime's own inspect format, must yield the identical []string. A value with a
// trailing space and a value containing a newline are the two shapes that used to
// disagree.
func TestInspectEnvAgreesAcrossRuntimes(t *testing.T) {
	want := []string{"A=1", "B= 2 ", "C=x", "D=3"}

	appleRaw := `[{"configuration":{"initProcess":{"environment":["A=1","B= 2 ","C=x\nD=3"]}}}]`
	dockerRaw := "A=1\nB= 2 \nC=x\nD=3\n"

	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeInspectStub(t, dir, "container", appleRaw)
	writeInspectStub(t, dir, "docker", dockerRaw)

	apple, err := NewApple(Darwin).InspectEnv(context.Background(), "c")
	if err != nil {
		t.Fatalf("Apple.InspectEnv: %v", err)
	}
	docker, err := NewDocker(Darwin).InspectEnv(context.Background(), "c")
	if err != nil {
		t.Fatalf("Docker.InspectEnv: %v", err)
	}

	if !reflect.DeepEqual(apple, want) {
		t.Errorf("Apple.InspectEnv() = %q, want %q", apple, want)
	}
	if !reflect.DeepEqual(docker, want) {
		t.Errorf("Docker.InspectEnv() = %q, want %q", docker, want)
	}
	if !reflect.DeepEqual(apple, docker) {
		t.Errorf("the two runtimes disagree: apple %q, docker %q", apple, docker)
	}
}

// bash reads inspect output with `while IFS= read -r`, which does not trim. This
// is the regression guard for using splitLines (which trims, and is right for
// container *names*) on an environment value.
func TestDockerInspectEnvPreservesWhitespace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeInspectStub(t, dir, "docker", "A= 1 \n")

	got, err := NewDocker(Darwin).InspectEnv(context.Background(), "c")
	if err != nil {
		t.Fatalf("InspectEnv: %v", err)
	}
	if want := []string{"A= 1 "}; !reflect.DeepEqual(got, want) {
		t.Errorf("InspectEnv() = %q, want %q", got, want)
	}
}

// A failing probe is "no environment", never an error: bash wraps both in
// `2>/dev/null || true`.
func TestInspectEnvFailureIsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, bin := range []string{"container", "docker"} {
		writeStub(t, dir, bin, "#!/bin/sh\nexit 1\n")
	}

	for _, rt := range []Runtime{NewApple(Darwin), NewDocker(Darwin)} {
		got, err := rt.InspectEnv(context.Background(), "c")
		if err != nil {
			t.Errorf("%s: InspectEnv returned an error: %v", rt.Profile().Name, err)
		}
		if got != nil {
			t.Errorf("%s: InspectEnv() = %q, want nil", rt.Profile().Name, got)
		}
	}
}

// writeInspectStub installs a runtime stub whose `inspect` prints out verbatim.
func writeInspectStub(t *testing.T, dir, name, out string) {
	t.Helper()
	payload := filepath.Join(dir, name+".inspect")
	if err := os.WriteFile(payload, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	writeStub(t, dir, name, "#!/bin/sh\nif [ \"$1\" = inspect ]; then cat "+payload+"; fi\nexit 0\n")
}

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
