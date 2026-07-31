package runtime

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// The whole point of injecting the platform is that these tables run identically
// on a macOS developer machine and on CI's Linux runner. Nothing here may read
// runtime.GOOS.

// wantBin discriminates which runtime Select returned. Profile().Name cannot
// do this any more: ticket 11 gave both runtimes the same program name
// (ProgName), so Bin() -- "container" for Apple, "docker" for Docker -- is the
// only observable difference left.
func TestSelectPrecedence(t *testing.T) {
	cases := []struct {
		name    string
		sel     Selection
		wantBin string
	}{
		{"flag beats env", Selection{Flag: "apple", Env: "docker", Platform: Darwin}, "container"},
		{"flag beats argv0", Selection{Flag: "apple", Argv0: "claude-contained-docked", Platform: Darwin}, "container"},
		{"env beats argv0", Selection{Env: "docker", Argv0: "claude-contained", Platform: Darwin}, "docker"},
		{"argv0 docked", Selection{Argv0: "bin/claude-contained-docked", Platform: Darwin}, "docker"},
		{"argv0 docked absolute", Selection{Argv0: "/opt/bin/claude-docked", Platform: Darwin}, "docker"},
		{"argv0 plain on darwin defaults to apple", Selection{Argv0: "claude-contained", Platform: Darwin}, "container"},
		{"argv0 plain absolute on darwin", Selection{Argv0: "/usr/local/bin/claude-contained", Platform: Darwin}, "container"},

		// The rows that pin the actual fix. A basename without "dock" is not a
		// selection, so it falls through to the platform default -- which cannot
		// be Apple Containers on a host that has none.
		{"argv0 plain on linux defaults to docker", Selection{Argv0: "claude-contained", Platform: Linux}, "docker"},
		{"apple-named argv0 on linux still defaults to docker", Selection{Argv0: "claude-contained", Platform: Linux}, "docker"},
		{"unknown platform defaults to docker", Selection{Argv0: "claude-contained"}, "docker"},

		{"value case is ignored", Selection{Flag: "DOCKER", Platform: Darwin}, "docker"},
		{"env case is ignored", Selection{Env: "Apple", Platform: Darwin}, "container"},
		{"nothing at all on darwin", Selection{Platform: Darwin}, "container"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Select(tc.sel).Bin(); got != tc.wantBin {
				t.Errorf("Select(%+v) ran %q, want %q", tc.sel, got, tc.wantBin)
			}
		})
	}
}

// Select must never fail: it runs before the command line is parsed, so a
// runtime has to exist even on the path that is about to exit 2.
func TestSelectIsTotal(t *testing.T) {
	// "dockerr" is not a Docker selection -- the substring rule is for basenames
	// only -- so this falls through to argv[0].
	sel := Selection{Flag: "dockerr", Argv0: "claude-contained-docked", Platform: Darwin}
	if got := Select(sel).Bin(); got != "docker" {
		t.Errorf("unrecognized flag value should fall through to argv[0], got %q", got)
	}

	// And with nothing else to fall through to, the platform default applies.
	if got := Select(Selection{Env: "bogus", Platform: Darwin}).Bin(); got != "container" {
		t.Errorf("unrecognized env value should fall through to the default, got %q", got)
	}
}

// An apple selection off macOS is *selected* so that --help still describes the
// runtime the user asked about; ValidateSelection is what refuses it.
func TestSelectAppleOffMacOSStillSelectsApple(t *testing.T) {
	if got := Select(Selection{Flag: "apple", Platform: Linux}).Bin(); got != "container" {
		t.Errorf("apple flag on linux ran %q, want container", got)
	}
}

// The platform must actually reach the constructed runtime, not be dropped on the
// way -- the host-gateway mapping is the cheapest observable proof.
func TestSelectPassesPlatformToRuntime(t *testing.T) {
	spec := RunSpec{Args: []Arg{HostGatewayArg{}}, Image: "img"}

	linux := Select(Selection{Flag: "docker", Platform: Linux}).RenderRun(spec)
	if !containsArg(linux, "--add-host") {
		t.Errorf("Docker/Linux lost the host-gateway mapping: %v", linux)
	}

	darwin := Select(Selection{Flag: "docker", Platform: Darwin}).RenderRun(spec)
	if containsArg(darwin, "--add-host") {
		t.Errorf("Docker/darwin should not map the host gateway: %v", darwin)
	}
}

func TestValidateSelection(t *testing.T) {
	cases := []struct {
		name    string
		sel     Selection
		wantErr bool
		want    []string
	}{
		{"no selection", Selection{Platform: Darwin}, false, nil},
		{"valid flag", Selection{Flag: "docker", Platform: Darwin}, false, nil},
		{"valid env", Selection{Env: "apple", Platform: Darwin}, false, nil},
		{"apple on darwin", Selection{Flag: "apple", Platform: Darwin}, false, nil},

		// Only the source actually used is checked: that is what "the flag wins"
		// has to mean to be useful.
		{"valid flag rescues a broken env", Selection{Flag: "docker", Env: "bogus", Platform: Darwin}, false, nil},

		{"bad flag", Selection{Flag: "bogus", Platform: Darwin}, true,
			[]string{"error: --container-runtime must be apple or docker: bogus"}},
		{"bad env", Selection{Env: "bogus", Platform: Darwin}, true,
			[]string{"error: CLAUDE_CONTAINED_RUNTIME must be apple or docker: bogus"}},
		{"bad flag beats valid env", Selection{Flag: "bogus", Env: "docker", Platform: Darwin}, true,
			[]string{"error: --container-runtime must be apple or docker: bogus"}},
		{"apple off macOS", Selection{Flag: "apple", Platform: Linux}, true, []string{
			"error: the apple container runtime is available only on macOS",
			"       use --container-runtime=docker or CLAUDE_CONTAINED_RUNTIME=docker",
		}},
		{"apple off macOS via env", Selection{Env: "apple", Platform: Linux}, true, []string{
			"error: the apple container runtime is available only on macOS",
			"       use --container-runtime=docker or CLAUDE_CONTAINED_RUNTIME=docker",
		}},
		{"apple on unknown platform", Selection{Flag: "apple"}, true, []string{
			"error: the apple container runtime is available only on macOS",
			"       use --container-runtime=docker or CLAUDE_CONTAINED_RUNTIME=docker",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			err := ValidateSelection(tc.sel, &stderr)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateSelection(%+v) error = %v, wantErr %v", tc.sel, err, tc.wantErr)
			}
			var got []string
			if s := strings.TrimSuffix(stderr.String(), "\n"); s != "" {
				got = strings.Split(s, "\n")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("stderr = %q, want %q", got, tc.want)
			}
		})
	}
}

// SSH forwarding is the behavior that differs in all three supported
// configurations, so it gets the full matrix. The Linux arm is why the platform
// is injected: it cannot be reached on this host any other way.
func TestSSHRenderingPerConfiguration(t *testing.T) {
	const sock = "/tmp/agent.sock"

	cases := []struct {
		name     string
		rt       Runtime
		sshSock  string
		wantTail []string
	}{
		{"apple darwin", NewApple(Darwin), sock, []string{"--ssh"}},
		{"apple linux", NewApple(Linux), sock, []string{"--ssh"}},
		{"docker darwin ignores the host socket", NewDocker(Darwin), sock, []string{
			"--mount", "type=bind,src=/run/host-services/ssh-auth.sock,dst=/ssh-agent",
			"-e", "SSH_AUTH_SOCK=/ssh-agent",
		}},
		// -v, not --mount: the one bind in the launcher not expressed as --mount.
		{"docker linux mounts the real socket", NewDocker(Linux), sock, []string{
			"-v", sock + ":/ssh-agent", "-e", "SSH_AUTH_SOCK=/ssh-agent",
		}},
		{"docker linux without an agent emits nothing", NewDocker(Linux), "", nil},
		// The catch-all else arm: unlike host gateway, the SSH branch is
		// `== Darwin` with an else, so an unrecognized platform behaves as Linux.
		{"docker unknown platform behaves as linux", NewDocker(""), sock, []string{
			"-v", sock + ":/ssh-agent", "-e", "SSH_AUTH_SOCK=/ssh-agent",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SSH_AUTH_SOCK", tc.sshSock)
			got := tc.rt.RenderRun(RunSpec{Args: []Arg{SSHArg{}}, Image: "img"})
			want := append(append([]string{tc.rt.Bin(), "run", "--rm", "-it"}, tc.wantTail...), "img")
			if !reflect.DeepEqual(got, want) {
				t.Errorf("RenderRun() = %v, want %v", got, want)
			}
		})
	}
}

// Off by default, in every configuration, even with an agent in the environment.
func TestSSHIsOffByDefault(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")

	for _, rt := range []Runtime{NewApple(Darwin), NewApple(Linux), NewDocker(Darwin), NewDocker(Linux), NewDocker("")} {
		argv := rt.RenderRun(RunSpec{Args: []Arg{MemoryArg{Value: "8g"}}, Image: "img"})
		joined := strings.Join(argv, " ")
		for _, forbidden := range []string{"--ssh", "/ssh-agent", "SSH_AUTH_SOCK"} {
			if strings.Contains(joined, forbidden) {
				t.Errorf("%s: SSH forwarding leaked without -S: %v", rt.Bin(), argv)
			}
		}
	}
}

// Exactly Docker on Linux. The `== Linux` test has no else, so an unrecognized
// platform gets nothing -- deliberately asymmetric with the SSH branch above.
func TestHostGatewayIsDockerLinuxOnly(t *testing.T) {
	cases := []struct {
		name     string
		rt       Runtime
		wantTail []string
	}{
		{"docker linux", NewDocker(Linux), []string{"--add-host", "host.docker.internal:host-gateway"}},
		{"docker darwin", NewDocker(Darwin), nil},
		{"docker unknown platform", NewDocker(""), nil},
		{"apple darwin", NewApple(Darwin), nil},
		{"apple linux", NewApple(Linux), nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.rt.RenderRun(RunSpec{Args: []Arg{HostGatewayArg{}}, Image: "img"})
			want := append(append([]string{tc.rt.Bin(), "run", "--rm", "-it"}, tc.wantTail...), "img")
			if !reflect.DeepEqual(got, want) {
				t.Errorf("RenderRun() = %v, want %v", got, want)
			}
		})
	}
}

// Labels are Docker-only and informational; discovery reads the environment,
// which both runtimes carry. The platform must not enter into it.
func TestZellijLabelsArePlatformIndependent(t *testing.T) {
	spec := RunSpec{
		Args: []Arg{
			LabelArg{Key: "claude-contained.zellij", Value: "1"},
			LabelArg{Key: "claude-contained.zellij.session", Value: "alpha"},
		},
		Image: "img",
	}

	for _, p := range []Platform{Darwin, Linux, ""} {
		docker := NewDocker(p).RenderRun(spec)
		if got := countArg(docker, "--label"); got != 2 {
			t.Errorf("Docker/%q emitted %d labels, want 2: %v", p, got, docker)
		}
		if apple := NewApple(p).RenderRun(spec); containsArg(apple, "--label") {
			t.Errorf("Apple/%q emitted labels: %v", p, apple)
		}
	}
}

// The capability difference, as data on the Profile.
func TestHostForwardNoticeIsAppleOnly(t *testing.T) {
	want := []string{
		"Warning: Apple Containers cannot reach host services bound only to 127.0.0.1.",
		"         -H reaches host services listening on 0.0.0.0; use Docker for the rest.",
	}

	for _, p := range []Platform{Darwin, Linux} {
		if got := NewApple(p).Profile().HostForwardNotice; !reflect.DeepEqual(got, want) {
			t.Errorf("Apple/%q notice = %q, want %q", p, got, want)
		}
		if got := NewDocker(p).Profile().HostForwardNotice; got != nil {
			t.Errorf("Docker/%q should have no notice, got %q", p, got)
		}
	}
}

func containsArg(argv []string, want string) bool { return countArg(argv, want) > 0 }

func countArg(argv []string, want string) int {
	n := 0
	for _, a := range argv {
		if a == want {
			n++
		}
	}
	return n
}
