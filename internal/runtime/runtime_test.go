package runtime

import (
	"reflect"
	"testing"
)

// argv[0] is the selector precisely so that no flag the bash launchers do not
// know has to enter the CLI surface.
func TestSelectByArgv0(t *testing.T) {
	cases := []struct {
		argv0 string
		want  string
	}{
		{"claude-go", "claude-contained"},
		{"bin/claude-go", "claude-contained"},
		{"/usr/local/bin/claude-contained", "claude-contained"},
		{"claude-go-docked", "claude-docked"},
		{"bin/claude-go-docked", "claude-docked"},
		{"/opt/bin/claude-docked", "claude-docked"},
	}

	for _, tc := range cases {
		if got := Select(tc.argv0, "", "").Profile().Name; got != tc.want {
			t.Errorf("Select(%q) selected %q, want %q", tc.argv0, got, tc.want)
		}
	}
}

// Ticket 09 adds explicit selection; the precedence is already wired.
func TestSelectOverridePrecedence(t *testing.T) {
	if got := Select("claude-go", "docker", "").Profile().Name; got != "claude-docked" {
		t.Errorf("env override ignored, got %q", got)
	}
	if got := Select("claude-go-docked", "", "apple").Profile().Name; got != "claude-contained" {
		t.Errorf("flag override ignored, got %q", got)
	}
	if got := Select("claude-go", "apple", "docker").Profile().Name; got != "claude-docked" {
		t.Errorf("flag should beat env, got %q", got)
	}
}

// Mount syntax is deliberately *not* a divergence: both runtimes take the same
// form, read-only marker included.
func TestMountRenderingIsShared(t *testing.T) {
	spec := RunSpec{
		Args: []Arg{
			MountArg{Src: "/a", Dst: "/a"},
			MountArg{Src: "/b", Dst: "/b", ReadOnly: true},
		},
		Image:   "img",
		Command: []string{"sh"},
	}

	apple := NewApple().RenderRun(spec)
	docker := NewDocker().RenderRun(spec)

	want := []string{
		"run", "--rm", "-it",
		"--mount", "type=bind,src=/a,dst=/a",
		"--mount", "type=bind,src=/b,dst=/b,readonly",
		"img", "sh",
	}
	if !reflect.DeepEqual(apple[1:], want) {
		t.Errorf("apple rendered %#v, want %#v", apple[1:], want)
	}
	if !reflect.DeepEqual(docker[1:], want) {
		t.Errorf("docker rendered %#v, want %#v", docker[1:], want)
	}
	if apple[0] != "container" || docker[0] != "docker" {
		t.Errorf("wrong binaries: %q / %q", apple[0], docker[0])
	}
}

// Exec rendering is shared too: both runtimes emit `-it -u dev -e K=V` in the
// same order, differing only in the binary.
func TestExecRenderingIsShared(t *testing.T) {
	spec := ExecSpec{
		Container: "aic-alpha",
		User:      "dev",
		TTY:       true,
		Env: []EnvArg{
			{Key: "HOME", Value: "/h"},
			{Key: "FOO", Value: "bar"},
		},
		Command: []string{"srt-run", "/opt/claude/claude"},
	}

	apple := NewApple().RenderExec(spec)
	docker := NewDocker().RenderExec(spec)

	want := []string{
		"exec", "-it", "-u", "dev",
		"-e", "HOME=/h", "-e", "FOO=bar",
		"aic-alpha", "srt-run", "/opt/claude/claude",
	}
	if !reflect.DeepEqual(apple[1:], want) {
		t.Errorf("apple rendered %#v, want %#v", apple[1:], want)
	}
	if !reflect.DeepEqual(docker[1:], want) {
		t.Errorf("docker rendered %#v, want %#v", docker[1:], want)
	}
	if apple[0] != "container" || docker[0] != "docker" {
		t.Errorf("wrong binaries: %q / %q", apple[0], docker[0])
	}
}

// The genuine divergences: Apple has a dedicated SSH flag and no labels, while
// Docker records labels and needs a socket bind.
func TestRuntimeSpecificRendering(t *testing.T) {
	spec := RunSpec{
		Args:  []Arg{SSHArg{}, LabelArg{Key: "k", Value: "v"}},
		Image: "img",
	}

	apple := NewApple().RenderRun(spec)
	if !contains(apple, "--ssh") {
		t.Error("Apple Containers should use its dedicated --ssh flag")
	}
	if contains(apple, "--label") {
		t.Error("Apple Containers has no label concept")
	}

	docker := NewDocker().RenderRun(spec)
	if contains(docker, "--ssh") {
		t.Error("Docker has no --ssh flag")
	}
	if !contains(docker, "--label") {
		t.Error("Docker should record the label")
	}
}

// Apple Containers points at an often-unreachable vmnet gateway, so it forces a
// resolver; Docker keeps its own.
func TestDNSDefaultsDifferPerRuntime(t *testing.T) {
	if got := NewApple().Profile().DefaultDNS; !reflect.DeepEqual(got, []string{"1.1.1.1"}) {
		t.Errorf("apple DefaultDNS = %#v", got)
	}
	if got := NewDocker().Profile().DefaultDNS; got != nil {
		t.Errorf("docker DefaultDNS = %#v, want none", got)
	}
}

// The help texts differ by far more than the program name -- description line,
// DNS paragraph, a Docker-only build block -- so they are two literal texts.
func TestHelpTextsAreDistinct(t *testing.T) {
	apple := NewApple().Profile().Help
	docker := NewDocker().Profile().Help

	if apple == "" || docker == "" {
		t.Fatal("help text is empty")
	}
	if apple == docker {
		t.Error("the two runtimes must not share one help text")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
