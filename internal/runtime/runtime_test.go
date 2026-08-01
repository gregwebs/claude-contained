package runtime

import (
	"reflect"
	"strings"
	"testing"
)

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

	apple := NewApple(Darwin).RenderRun(spec)
	docker := NewDocker(Darwin).RenderRun(spec)

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

// TestZellijLabelsAreDockerOnly mirrors TestMountRenderingIsShared, but for
// the one arg that is *not* shared: the same RunSpec with a LabelArg renders
// --label under Docker and nothing at all under Apple Containers, which has
// no label concept. Discovery must never depend on this (ADR-0002) -- it is
// emitted for external tooling only.
func TestZellijLabelsAreDockerOnly(t *testing.T) {
	spec := RunSpec{
		Args: []Arg{
			LabelArg{Key: "claude-contained.zellij", Value: "1"},
			LabelArg{Key: "claude-contained.zellij.session", Value: "review"},
		},
		Image:   "img",
		Command: []string{"sh"},
	}

	apple := NewApple(Darwin).RenderRun(spec)
	docker := NewDocker(Darwin).RenderRun(spec)

	wantApple := []string{"run", "--rm", "-it", "img", "sh"}
	if !reflect.DeepEqual(apple[1:], wantApple) {
		t.Errorf("apple rendered %#v, want %#v", apple[1:], wantApple)
	}

	wantDocker := []string{
		"run", "--rm", "-it",
		"--label", "claude-contained.zellij=1",
		"--label", "claude-contained.zellij.session=review",
		"img", "sh",
	}
	if !reflect.DeepEqual(docker[1:], wantDocker) {
		t.Errorf("docker rendered %#v, want %#v", docker[1:], wantDocker)
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

	apple := NewApple(Darwin).RenderExec(spec)
	docker := NewDocker(Darwin).RenderExec(spec)

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

	apple := NewApple(Darwin).RenderRun(spec)
	if !contains(apple, "--ssh") {
		t.Error("Apple Containers should use its dedicated --ssh flag")
	}
	if contains(apple, "--label") {
		t.Error("Apple Containers has no label concept")
	}

	docker := NewDocker(Darwin).RenderRun(spec)
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
	if got := NewApple(Darwin).Profile().DefaultDNS; !reflect.DeepEqual(got, []string{"1.1.1.1"}) {
		t.Errorf("apple DefaultDNS = %#v", got)
	}
	if got := NewDocker(Darwin).Profile().DefaultDNS; got != nil {
		t.Errorf("docker DefaultDNS = %#v, want none", got)
	}
}

// Ticket 11 dropped the second launcher name: both runtimes are installed
// under the same program name, and a Docker user selects the runtime with
// --container-runtime or CLAUDE_CONTAINED_RUNTIME instead of a different
// binary name. This is the single assertion that makes "the Docker
// launcher's name no longer exists" executable.
func TestBothRuntimesUseThePrimaryProgramName(t *testing.T) {
	for name, p := range map[string]Profile{
		"apple":  NewApple(Darwin).Profile(),
		"docker": NewDocker(Darwin).Profile(),
	} {
		if p.Name != ProgName {
			t.Errorf("%s Profile.Name = %q, want %q", name, p.Name, ProgName)
		}
		if strings.Contains(p.Help, "claude-docked") {
			t.Errorf("%s help still names the retired Docker launcher", name)
		}
	}
}

// The help texts differ by far more than the program name -- description line,
// DNS paragraph, a Docker-only build block -- so they are two literal texts.
func TestHelpTextsAreDistinct(t *testing.T) {
	apple := NewApple(Darwin).Profile().Help
	docker := NewDocker(Darwin).Profile().Help

	if apple == "" || docker == "" {
		t.Fatal("help text is empty")
	}
	if apple == docker {
		t.Error("the two runtimes must not share one help text")
	}
}

// RenderBuild rendering is shared too, mirroring TestMountRenderingIsShared:
// both runtimes emit `build [--pull] [--no-cache] [--build-arg K=V]... -t TAG
// CTX` in that order, differing only in the binary.
func TestRenderBuildIsSharedApartFromTheBinary(t *testing.T) {
	cases := []struct {
		name string
		spec BuildSpec
		want []string
	}{
		{
			"tools refresh",
			BuildSpec{Tag: "claude-contained:latest", Context: "/ctx", BuildArgs: []string{"AI_TOOLS_CACHE_BUST=20260729211507"}},
			[]string{"build", "--build-arg", "AI_TOOLS_CACHE_BUST=20260729211507", "-t", "claude-contained:latest", "/ctx"},
		},
		{
			"full rebuild",
			BuildSpec{Tag: "claude-contained:latest", Context: "/ctx", Pull: true, NoCache: true},
			[]string{"build", "--pull", "--no-cache", "-t", "claude-contained:latest", "/ctx"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apple := NewApple(Darwin).RenderBuild(tc.spec)
			docker := NewDocker(Darwin).RenderBuild(tc.spec)

			if !reflect.DeepEqual(apple[1:], tc.want) {
				t.Errorf("apple rendered %#v, want %#v", apple[1:], tc.want)
			}
			if !reflect.DeepEqual(docker[1:], tc.want) {
				t.Errorf("docker rendered %#v, want %#v", docker[1:], tc.want)
			}
			if apple[0] != "container" || docker[0] != "docker" {
				t.Errorf("wrong binaries: %q / %q", apple[0], docker[0])
			}
		})
	}
}

// The help fixtures also carry the build-context additions, alongside the
// runtime-selection ones TestHelpDocumentsRuntimeSelection already pins.
// Regenerate with the same diff command documented there.
func TestHelpDocumentsBuildContext(t *testing.T) {
	apple := NewApple(Darwin).Profile().Help
	docker := NewDocker(Darwin).Profile().Help

	for name, help := range map[string]string{"apple": apple, "docker": docker} {
		if !strings.Contains(help, "--build-context") {
			t.Errorf("%s help does not document --build-context", name)
		}
		if !strings.Contains(help, "CLAUDE_CONTAINED_BUILD_CONTEXT") {
			t.Errorf("%s help does not document CLAUDE_CONTAINED_BUILD_CONTEXT", name)
		}
	}
}

func TestHelpDocumentsDiagnosticStream(t *testing.T) {
	for name, help := range map[string]string{
		"apple":  NewApple(Darwin).Profile().Help,
		"docker": NewDocker(Darwin).Profile().Help,
	} {
		for _, text := range []string{
			"--log-level LEVEL",
			"--log-file PATH",
			"--log-only",
			"CLAUDE_CONTAINED_LOG_LEVEL",
			"mode 0600",
		} {
			if !strings.Contains(help, text) {
				t.Errorf("%s help does not document %q", name, text)
			}
		}
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

// The help_*.txt fixtures are the source of truth for --help text; there is no
// longer a bash script to diff against, so changes go through the .txt files
// and the assertions here and in TestHelpDocumentsBuildContext.
func TestHelpDocumentsRuntimeSelection(t *testing.T) {
	apple := NewApple(Darwin).Profile().Help
	docker := NewDocker(Darwin).Profile().Help

	for name, help := range map[string]string{"apple": apple, "docker": docker} {
		if !strings.Contains(help, "--container-runtime") {
			t.Errorf("%s help does not document --container-runtime", name)
		}
		if !strings.Contains(help, "CLAUDE_CONTAINED_RUNTIME") {
			t.Errorf("%s help does not document CLAUDE_CONTAINED_RUNTIME", name)
		}
		// Discoverability, not just presence: the flag must be in the Options
		// block, not merely mentioned in Notes prose, or a Docker user has no
		// way to find the flag that replaced the second launcher name.
		if !strings.Contains(help, "  --container-runtime NAME") {
			t.Errorf("%s help does not list --container-runtime in the Options block", name)
		}
	}

	// The -H caveat is a property of Apple Containers, so only its help carries it
	// -- and that half *is* byte-identical to the bash text.
	const caveat = "bound only to 127.0.0.1"
	if !strings.Contains(apple, caveat) {
		t.Errorf("apple help does not carry the -H caveat %q", caveat)
	}
	if strings.Contains(docker, caveat) {
		t.Error("docker help should not carry the -H caveat: Docker reaches those services")
	}
}
