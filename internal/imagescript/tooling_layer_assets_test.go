package imagescript

// Verifies the shipped Java tooling-layer example, the default devcontainer, and
// the repository/go tooling layers as standalone assets: pinned and
// SHA-verified tool downloads, architecture selection, PATH-reset link commands,
// launcher-fragment/direct-image environment parity, Maven shared-state (no
// dedicated .m2 mount), a toolchain-neutral base image and devcontainer, and the
// pinned versions the quality gate depends on. Most assertions read the shipped
// file and match structurally; two source mavenrc under a real sh to prove HOME
// expansion, exactly as a direct-image Maven run would.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(repoFile(t, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

func mustContain(t *testing.T, rel, want string) {
	t.Helper()
	if !strings.Contains(readRepoFile(t, rel), want) {
		t.Errorf("%s does not contain %q", rel, want)
	}
}

func mustNotContain(t *testing.T, rel string, pattern string) {
	t.Helper()
	if regexp.MustCompile(pattern).MatchString(readRepoFile(t, rel)) {
		t.Errorf("%s unexpectedly matches %q", rel, pattern)
	}
}

func mustMatch(t *testing.T, rel, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(readRepoFile(t, rel)) {
		t.Errorf("%s does not match %q", rel, pattern)
	}
}

const (
	javaDockerfile = "examples/tooling-layers/java/Dockerfile"
	javaFragment   = "examples/tooling-layers/java/10-java"
)

func TestJavaExampleIsCompleteBuildContext(t *testing.T) {
	for _, f := range []string{
		"examples/tooling-layers/java/Dockerfile",
		"examples/tooling-layers/java/10-java",
		"examples/tooling-layers/java/mavenrc",
		"examples/tooling-layers/java/hotswap-agent.properties",
		"examples/tooling-layers/java/README.md",
	} {
		if _, err := os.Stat(repoFile(t, f)); err != nil {
			t.Errorf("Java example is missing %s: %v", f, err)
		}
	}
}

func TestJavaExampleConfigurableBaseImage(t *testing.T) {
	mustContain(t, javaDockerfile, "ARG BASE_IMAGE=claude-contained:latest")
	mustContain(t, javaDockerfile, "FROM ${BASE_IMAGE}")
}

func TestJavaToolDownloadsPinned(t *testing.T) {
	for _, pin := range []string{
		"JBR_VERSION", "JBR_BUILD", "HOTSWAP_AGENT_VERSION", "JDTLS_VERSION",
		"JDTLS_TIMESTAMP", "MAVEN_VERSION", "JBANG_VERSION",
	} {
		mustMatch(t, javaDockerfile, `(?m)^ARG `+pin+`=\S+$`)
	}
}

func TestJavaArchSelectionAndVerification(t *testing.T) {
	for _, want := range []string{"arm64)", "amd64)", "Unsupported architecture", "sha256sum -c"} {
		mustContain(t, javaDockerfile, want)
	}
}

func TestJavaJBRPinnedSha256(t *testing.T) {
	mustMatch(t, javaDockerfile, `(?m)^ARG JBR_SHA256_ARM64=[[:xdigit:]]{64}$`)
	mustMatch(t, javaDockerfile, `(?m)^ARG JBR_SHA256_AMD64=[[:xdigit:]]{64}$`)
	mustMatch(t, javaDockerfile, `arm64\).*jbr_sha256=.*JBR_SHA256_ARM64`)
	mustMatch(t, javaDockerfile, `amd64\).*jbr_sha256=.*JBR_SHA256_AMD64`)
	mustContain(t, javaDockerfile, `printf '%s  %s\n' "$jbr_sha256" /tmp/jbr.tar.gz | sha256sum -c -`)
}

func TestJavaToolLinkCommands(t *testing.T) {
	body := readRepoFile(t, javaDockerfile)
	for _, cmd := range []string{"java", "javac", "jar", "mvn", "jbang", "jdtls"} {
		re := regexp.MustCompile(`ln -sf? .* /usr/local/bin/` + cmd + `([;[:space:]]|$)|"/usr/local/bin/` + cmd + `"`)
		if !re.MatchString(body) {
			t.Errorf("Java Dockerfile has no PATH-reset link command for %s", cmd)
		}
	}
}

func TestJavaFragmentDeclaresEnv(t *testing.T) {
	mustContain(t, javaDockerfile, "COPY 10-java /etc/claude-contained/env.d/10-java")
	mustMatch(t, javaFragment, `(?m)^JAVA_HOME=/opt/jbr$`)
	mustMatch(t, javaFragment, `(?m)^JAVA_TOOL_OPTIONS=.*AllowEnhancedClassRedefinition.*HotswapAgent=fatjar`)
	mustMatch(t, javaFragment, `(?m)^MAVEN_OPTS=-Dmaven\.repo\.local=\$HOME/\.claude-contained/cache/maven$`)
	mustMatch(t, javaFragment, `(?m)^PATH=/opt/jbr/bin:/opt/maven/bin:/opt/jbang/bin:\$PATH$`)
}

func TestJavaDirectImageEnvMatchesFragment(t *testing.T) {
	mustContain(t, javaDockerfile, "ENV JAVA_HOME=/opt/jbr")
	mustContain(t, javaDockerfile, "AllowEnhancedClassRedefinition")
	mustContain(t, javaDockerfile, "HotswapAgent=fatjar")
	mustContain(t, javaDockerfile, `ENV MAVEN_OPTS="-Dmaven.repo.local=\$HOME/.claude-contained/cache/maven"`)
	mustContain(t, javaDockerfile, `ENV PATH="/opt/jbr/bin:/opt/maven/bin:/opt/jbang/bin:${PATH}"`)
}

func TestJavaHotswapOptionsIdenticalAcrossSeams(t *testing.T) {
	fragOpts := regexp.MustCompile(`(?m)^JAVA_TOOL_OPTIONS=(.*)$`).FindStringSubmatch(readRepoFile(t, javaFragment))
	imageOpts := regexp.MustCompile(`(?m)^ENV JAVA_TOOL_OPTIONS="(.*)"$`).FindStringSubmatch(readRepoFile(t, javaDockerfile))
	if fragOpts == nil || imageOpts == nil {
		t.Fatalf("could not extract JAVA_TOOL_OPTIONS from both seams (fragment=%v image=%v)", fragOpts != nil, imageOpts != nil)
	}
	if fragOpts[1] == "" || fragOpts[1] != imageOpts[1] {
		t.Errorf("HotSwap options differ across startup seams:\nfragment: %q\nimage:    %q", fragOpts[1], imageOpts[1])
	}
}

func TestJavaMavenSharedStateNoM2(t *testing.T) {
	for _, f := range []string{javaDockerfile, javaFragment, "examples/tooling-layers/java/README.md"} {
		mustContain(t, f, ".claude-contained/cache/maven")
	}
	for _, f := range []string{javaDockerfile, javaFragment} {
		if strings.Contains(readRepoFile(t, f), ".m2") {
			t.Errorf("%s references a .m2 path; Maven state must use shared launcher state", f)
		}
	}
}

func TestBaseDockerfileHasNoJavaStage(t *testing.T) {
	mustNotContain(t, "Dockerfile", `INCLUDE_JAVA_LAYER|custom-packages|/opt/jbr|sdkman|JAVA_HOME|JAVA_TOOL_OPTIONS|MAVEN_OPTS`)
}

func TestDevcontainerNeutral(t *testing.T) {
	mustContain(t, "devcontainer/devcontainer.json", `"name": "Claude Contained"`)
	mustContain(t, "devcontainer/devcontainer.json", `"image": "claude-contained:latest"`)
	mustNotContain(t, "devcontainer/devcontainer.json", `(?i)(java|spring|vaadin|lombok|\.m2|\.vaadin|/opt/jbr|8080|5005)`)
}

func TestDevcontainerGuideDocumentsLayerBuild(t *testing.T) {
	for _, want := range []string{
		`"context": "../.claude-contained/layer/"`,
		`"BASE_IMAGE": "claude-contained:latest"`,
		"examples/tooling-layers/java",
	} {
		mustContain(t, "devcontainer/README.md", want)
	}
}

func TestToolingLayerPinnedVersions(t *testing.T) {
	t.Run("go_version", func(t *testing.T) {
		mustContain(t, "examples/tooling-layers/go/Dockerfile", "ARG GO_VERSION=1.24.3")
		mustContain(t, ".claude-contained/layer/Dockerfile", "ARG GO_VERSION=1.24.3")
	})
	t.Run("shellcheck", func(t *testing.T) {
		mustContain(t, ".claude-contained/layer/Dockerfile", "ARG SHELLCHECK_VERSION=0.11.0")
	})
	t.Run("golangci", func(t *testing.T) {
		mustContain(t, ".claude-contained/layer/Dockerfile", "ARG GOLANGCI_LINT_VERSION=2.12.2")
	})
	t.Run("sha256sum", func(t *testing.T) {
		mustContain(t, ".claude-contained/layer/Dockerfile", "sha256sum --check -")
		mustContain(t, "examples/tooling-layers/go/Dockerfile", "sha256sum --check -")
	})
	t.Run("go_mod", func(t *testing.T) {
		mustContain(t, "go.mod", "go 1.24")
	})
	t.Run("makefile_versions", func(t *testing.T) {
		mustContain(t, "Makefile", "SHELLCHECK_REQUIRED_VERSION := 0.11.0")
		mustContain(t, "Makefile", "GOLANGCI_LINT_REQUIRED_VERSION := 2.12.2")
	})
}

func TestToolingLayerGoFragmentIdentical(t *testing.T) {
	example := []byte(readRepoFile(t, "examples/tooling-layers/go/10-go"))
	repo := []byte(readRepoFile(t, ".claude-contained/layer/10-go"))
	if !bytes.Equal(example, repo) {
		t.Error("examples/tooling-layers/go/10-go and .claude-contained/layer/10-go have diverged")
	}
}

func TestToolingLayerGoFragmentCacheEnv(t *testing.T) {
	for _, want := range []string{
		"GOTOOLCHAIN=local",
		"GOCACHE=${HOME}/.claude-contained/cache/go-build",
		"GOMODCACHE=${HOME}/.claude-contained/cache/go-mod",
	} {
		mustContain(t, "examples/tooling-layers/go/10-go", want)
	}
}

// sourceMavenrc sources the example mavenrc under a real sh with the given HOME
// and MAVEN_OPTS, returning the resulting MAVEN_OPTS -- the direct-image Maven
// startup path.
func sourceMavenrc(t *testing.T, home, mavenOpts string) string {
	t.Helper()
	sh := requireSh(t)
	mavenrc := repoFile(t, "examples/tooling-layers/java/mavenrc")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, "-c", `. "$1"; printf "%s" "$MAVEN_OPTS"`, "sh", mavenrc)
	cmd.Env = []string{"HOME=" + home, "MAVEN_OPTS=" + mavenOpts, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sourcing mavenrc: %v", err)
	}
	return string(out)
}

func TestMavenrcExpandsHomeForDirectImage(t *testing.T) {
	got := sourceMavenrc(t, "/Users/path-parity-dev", `-Dmaven.repo.local=$HOME/.claude-contained/cache/maven`)
	want := "-Dmaven.repo.local=/Users/path-parity-dev/.claude-contained/cache/maven"
	if got != want {
		t.Errorf("mavenrc MAVEN_OPTS = %q, want %q", got, want)
	}
}

func TestMavenrcPreservesLauncherResolvedState(t *testing.T) {
	already := "-Dmaven.repo.local=/Users/path-parity-launcher/.claude-contained/cache/maven"
	if got := sourceMavenrc(t, "/Users/path-parity-launcher", already); got != already {
		t.Errorf("mavenrc rewrote already-resolved MAVEN_OPTS to %q, want it preserved as %q", got, already)
	}
}
