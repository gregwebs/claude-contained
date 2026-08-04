package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestAppleLocalTagLayerFlow is deliberately availability-gated rather than
// opt-in: when Apple Containers and the fixture image are present, the CLI
// contract needs exercising on the installed version. It owns only its unique
// source derivatives and never removes the shared base fixture.
func TestAppleLocalTagLayerFlow(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Containers is only available on macOS")
	}
	if _, err := exec.LookPath("container"); err != nil {
		t.Skip("Apple Containers CLI is unavailable")
	}
	if err := exec.Command("container", "system", "status").Run(); err != nil {
		t.Skip("Apple Containers service is unavailable")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	apple := NewApple(Darwin)
	const base = "claude-contained:latest"
	desc, present, err := apple.DescribeImage(ctx, base)
	if err != nil {
		t.Skipf("local fixture cannot be inspected: %v", err)
	}
	if !present {
		t.Skipf("local fixture %s is unavailable", base)
	}
	if desc.BuildRef != base || desc.BuildRefImmutable || strings.HasPrefix(desc.BuildRef, "sha256:") {
		t.Fatalf("Apple 1.1-compatible descriptor = %+v, want mutable local tag and never a standalone digest", desc)
	}

	version, _ := exec.Command("container", "system", "status").CombinedOutput()
	t.Logf("Apple Containers status:\n%s", strings.TrimSpace(string(version)))
	t.Logf("base identity: %s", desc.Identity)

	ctxDir := t.TempDir()
	if err := os.WriteFile(ctxDir+"/Dockerfile", []byte("ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := fmt.Sprintf("cc-layer-probe-%d", time.Now().UnixNano())
	stage, final, named := stamp+":stage", stamp+":final", stamp+":named"
	for _, ref := range []string{stage, final, named} {
		ref := ref
		t.Cleanup(func() { _ = exec.Command("container", "image", "rm", ref).Run() })
	}

	// Keep the historical named-digest capability probe. Apple 1.1.0 is
	// expected to reject it; a later success is evidence to explicitly enable
	// the immutable capability rather than a reason to change this test.
	namedRef := base + "@" + desc.Identity
	namedArgs := apple.RenderBuild(BuildSpec{Tag: named, Context: ctxDir, BuildArgs: []string{"BASE_IMAGE=" + namedRef}})
	if out, err := exec.CommandContext(ctx, namedArgs[0], namedArgs[1:]...).CombinedOutput(); err != nil {
		t.Logf("named-digest capability unavailable for %s: %s", namedRef, strings.TrimSpace(string(out)))
	} else {
		t.Logf("named-digest capability succeeded for %s", namedRef)
	}

	buildArgs := apple.RenderBuild(BuildSpec{Tag: stage, Context: ctxDir, BuildArgs: []string{"BASE_IMAGE=" + base}})
	if out, err := exec.CommandContext(ctx, buildArgs[0], buildArgs[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("guarded local-tag stage build failed: %v\n%s", err, out)
	}
	post, ok, err := apple.DescribeImage(ctx, base)
	if err != nil || !ok || post.Identity != desc.Identity {
		t.Fatalf("post-build base probe = (%+v, %v, %v), want unchanged %s", post, ok, err, desc.Identity)
	}
	if out, err := exec.CommandContext(ctx, apple.RenderTag(stage, final)[0], apple.RenderTag(stage, final)[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("stage-only promotion failed: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, apple.RenderRemove(stage)[0], apple.RenderRemove(stage)[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("stage cleanup failed: %v\n%s", err, out)
	}
	if _, ok, err := apple.DescribeImage(ctx, stage); err != nil || ok {
		t.Fatalf("stage remains after cleanup: present=%v err=%v", ok, err)
	}
	if _, ok, err := apple.DescribeImage(ctx, final); err != nil || !ok {
		t.Fatalf("final missing after stage cleanup: present=%v err=%v", ok, err)
	}
	if _, ok, err := apple.DescribeImage(ctx, base); err != nil || !ok {
		t.Fatalf("base missing after stage cleanup: present=%v err=%v", ok, err)
	}
}
