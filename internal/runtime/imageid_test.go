package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeImageIDStubs installs fake `container` and `docker` executables that
// answer only `image inspect`, driven by two environment variables so one
// script covers every arm of probeImageID's classification:
//
//	STUB_MODE=present  print an identifier in the shape that runtime parses
//	STUB_MODE=empty    exit 0 having printed nothing (the "fault" case)
//	STUB_MODE=missing  exit 1 with an error on stderr
//	STUB_HELP_EXIT=N   the status `image inspect --help` exits with
//
// The two runtimes parse different shapes, so the script dispatches on its own
// basename: Docker reads trimmed stdout, Apple reads JSON.
func writeImageIDStubs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"set -u\n" +
		"self=$(basename \"$0\")\n" +
		"[ \"${1:-}\" = image ] || exit 99\n" +
		"[ \"${2:-}\" = inspect ] || exit 99\n" +
		"shift 2\n" +
		"for a in \"$@\"; do\n" +
		"  [ \"$a\" = --help ] && exit \"${STUB_HELP_EXIT:-0}\"\n" +
		"done\n" +
		"case \"${STUB_MODE:-present}\" in\n" +
		"  present)\n" +
		"    if [ \"$self\" = container ]; then\n" +
		"      printf '[{\"descriptor\":{\"digest\":\"sha256:stub\"}}]\\n'\n" +
		"    else\n" +
		"      printf 'sha256:stub\\n'\n" +
		"    fi\n" +
		"    exit 0 ;;\n" +
		"  empty) exit 0 ;;\n" +
		"  *)\n" +
		"    printf 'Error response: No such image: %s\\n' \"$*\" >&2\n" +
		"    printf 'a second line nobody should see\\n' >&2\n" +
		"    exit 1 ;;\n" +
		"esac\n"

	for _, name := range []string{"container", "docker"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// imageIDRuntimes are the two implementers of the seam. Every case runs against
// both: the classification lives in probeImageID and must not drift per runtime.
func imageIDRuntimes() map[string]Runtime {
	return map[string]Runtime{
		"apple":  NewApple(Darwin),
		"docker": NewDocker(Linux),
	}
}

func TestDescribeImageReportsAPresentImage(t *testing.T) {
	for name, rt := range imageIDRuntimes() {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PATH", writeImageIDStubs(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("STUB_MODE", "present")

			desc, ok, err := rt.DescribeImage(t.Context(), "claude-contained:latest")
			if err != nil {
				t.Fatalf("DescribeImage: %v", err)
			}
			if !ok {
				t.Fatal("ok = false, want true: the stub reported an image")
			}
			if desc.Identity != "sha256:stub" {
				t.Errorf("identity = %q, want %q", desc.Identity, "sha256:stub")
			}
		})
	}
}

func TestImageDescriptorsSeparateIdentityFromAppleBuildReference(t *testing.T) {
	t.Setenv("PATH", writeImageIDStubs(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_MODE", "present")

	apple, ok, err := NewApple(Darwin).DescribeImage(t.Context(), "claude-contained:latest")
	if err != nil || !ok {
		t.Fatalf("apple DescribeImage = (%+v, %v, %v)", apple, ok, err)
	}
	if apple.Identity != "sha256:stub" || apple.BuildRef != "claude-contained:latest" || apple.BuildRefImmutable {
		t.Errorf("unverified Apple descriptor = %+v, want digest identity and mutable local tag", apple)
	}
	if strings.HasPrefix(apple.BuildRef, "sha256:") {
		t.Errorf("Apple build reference must never be a standalone digest: %q", apple.BuildRef)
	}

	docker, ok, err := NewDocker(Linux).DescribeImage(t.Context(), "claude-contained:latest")
	if err != nil || !ok {
		t.Fatalf("docker DescribeImage = (%+v, %v, %v)", docker, ok, err)
	}
	if docker.Identity != "sha256:stub" || docker.BuildRef != "sha256:stub" || !docker.BuildRefImmutable {
		t.Errorf("Docker descriptor = %+v, want immutable digest for both fields", docker)
	}
}

func TestAppleVerifiedCapabilityUsesNamedDigestReference(t *testing.T) {
	t.Setenv("PATH", writeImageIDStubs(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
	orig := appleDigestBuildRefSupported
	appleDigestBuildRefSupported = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { appleDigestBuildRefSupported = orig })

	desc, ok, err := NewApple(Darwin).DescribeImage(t.Context(), "claude-contained:latest")
	if err != nil || !ok {
		t.Fatalf("DescribeImage = (%+v, %v, %v)", desc, ok, err)
	}
	if desc.BuildRef != "claude-contained:latest@sha256:stub" || !desc.BuildRefImmutable {
		t.Errorf("descriptor = %+v, want immutable named digest", desc)
	}
}

// The case that used to be silently miscalled "absent". A probe that succeeds
// and yields nothing readable is a defect in *our* parsing, and reporting
// absence would send the caller to rebuild an image that is already there.
func TestDescribeImageTreatsAnUnreadableSuccessAsAFault(t *testing.T) {
	for name, rt := range imageIDRuntimes() {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PATH", writeImageIDStubs(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("STUB_MODE", "empty")

			desc, ok, err := rt.DescribeImage(t.Context(), "claude-contained:latest")
			if err == nil {
				t.Fatal("err = nil, want a fault: a successful probe we cannot read is not an absence")
			}
			if ok || desc.Identity != "" {
				t.Errorf("got (%q, %v), want (\"\", false)", desc.Identity, ok)
			}
			if !strings.Contains(err.Error(), rt.Bin()) {
				t.Errorf("err = %q, want it to name the binary %q", err, rt.Bin())
			}
		})
	}
}

func TestDescribeImageReportsARealAbsence(t *testing.T) {
	for name, rt := range imageIDRuntimes() {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PATH", writeImageIDStubs(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("STUB_MODE", "missing")
			t.Setenv("STUB_HELP_EXIT", "0")

			desc, ok, err := rt.DescribeImage(t.Context(), "claude-contained:latest")
			if err != nil {
				t.Fatalf("err = %v, want nil: the subcommand exists, so this is a genuine absence", err)
			}
			if ok || desc.Identity != "" {
				t.Errorf("got (%q, %v), want (\"\", false)", desc.Identity, ok)
			}
		})
	}
}

// The whole reason the capability probe exists: a CLI that spells the
// subcommand differently must produce a named fault, never "the base image is
// not built" on a machine where it is right there.
func TestDescribeImageTreatsAnUnknownSubcommandAsAFault(t *testing.T) {
	for name, rt := range imageIDRuntimes() {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PATH", writeImageIDStubs(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("STUB_MODE", "missing")
			t.Setenv("STUB_HELP_EXIT", "64")

			desc, ok, err := rt.DescribeImage(t.Context(), "claude-contained:latest")
			if err == nil {
				t.Fatal("err = nil, want a fault naming the subcommand")
			}
			if ok || desc.Identity != "" {
				t.Errorf("got (%q, %v), want (\"\", false)", desc.Identity, ok)
			}
			for _, want := range []string{rt.Bin(), "image inspect"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to mention %q", err, want)
				}
			}
			// One line, not a pasted usage text: the caller prints this.
			if strings.Contains(err.Error(), "a second line nobody should see") {
				t.Errorf("err = %q, want only the first stderr line", err)
			}
		})
	}
}

func TestDescribeImageWithNoBinaryOnPathIsAFault(t *testing.T) {
	for name, rt := range imageIDRuntimes() {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())

			desc, ok, err := rt.DescribeImage(t.Context(), "claude-contained:latest")
			if err == nil {
				t.Fatal("err = nil, want a fault: an unrunnable probe answered nothing")
			}
			if ok || desc.Identity != "" {
				t.Errorf("got (%q, %v), want (\"\", false)", desc.Identity, ok)
			}
		})
	}
}
