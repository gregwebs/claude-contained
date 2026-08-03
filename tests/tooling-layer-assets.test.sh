#!/usr/bin/env bash
# Verifies the shipped Java tooling layer and generic devcontainer as standalone assets.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
example="${repo_root}/examples/tooling-layers/java"
dockerfile="${example}/Dockerfile"
fragment="${example}/10-java"
mavenrc="${example}/mavenrc"
devcontainer="${repo_root}/devcontainer/devcontainer.json"
passes=0 failures=0
pin_status=0 link_status=0

check() {
  local name="$1" status="$2"
  if [[ "$status" -eq 0 ]]; then
    passes=$((passes + 1))
  else
    printf 'FAIL: %s\n' "$name" >&2
    failures=$((failures + 1))
  fi
}

if [[ -f "$dockerfile" && -f "$fragment" && -f "$mavenrc" && -f "${example}/hotswap-agent.properties" && -f "${example}/README.md" ]]; then status=0; else status=1; fi
check "Java example is a complete build context" "$status"

head -n 2 "$dockerfile" | grep -Fqx 'ARG BASE_IMAGE=claude-contained:latest' \
  && head -n 2 "$dockerfile" | grep -Fqx 'FROM ${BASE_IMAGE}'
check "example builds independently from a configurable base image" $?

for pin in JBR_VERSION JBR_BUILD HOTSWAP_AGENT_VERSION JDTLS_VERSION JDTLS_TIMESTAMP MAVEN_VERSION JBANG_VERSION; do
  grep -Eq "^ARG ${pin}=[^[:space:]]+$" "$dockerfile" || pin_status=1
done
check "every Java tool download has an explicit version pin" "$pin_status"

grep -Fq 'arm64)' "$dockerfile" && grep -Fq 'amd64)' "$dockerfile" \
  && grep -Fq 'Unsupported architecture' "$dockerfile" \
  && grep -Fq 'sha256sum -c' "$dockerfile"
check "both architectures are selected explicitly and downloads are verified" $?

grep -Eq '^ARG JBR_SHA256_ARM64=[[:xdigit:]]{64}$' "$dockerfile" \
  && grep -Eq '^ARG JBR_SHA256_AMD64=[[:xdigit:]]{64}$' "$dockerfile" \
  && grep -Eq 'arm64\).*jbr_sha256=.*JBR_SHA256_ARM64' "$dockerfile" \
  && grep -Eq 'amd64\).*jbr_sha256=.*JBR_SHA256_AMD64' "$dockerfile" \
  && grep -Fq "printf '%s  %s\\n' \"\$jbr_sha256\" /tmp/jbr.tar.gz | sha256sum -c -" "$dockerfile"
check "the JBR archive uses a pinned architecture-specific SHA-256" $?

for command in java javac jar mvn jbang jdtls; do
  grep -Eq "ln -s[f]? .* /usr/local/bin/${command}([;[:space:]]|$)|\"/usr/local/bin/${command}\"" "$dockerfile" || link_status=1
done
check "PATH-reset command links cover Java runtime, build tools, and language server" "$link_status"

grep -Fq 'COPY 10-java /etc/claude-contained/env.d/10-java' "$dockerfile" \
  && grep -Eq '^JAVA_HOME=/opt/jbr$' "$fragment" \
  && grep -Eq '^JAVA_TOOL_OPTIONS=.*AllowEnhancedClassRedefinition.*HotswapAgent=fatjar' "$fragment" \
  && grep -Fqx 'MAVEN_OPTS=-Dmaven.repo.local=$HOME/.claude-contained/cache/maven' "$fragment" \
  && grep -Fqx 'PATH=/opt/jbr/bin:/opt/maven/bin:/opt/jbang/bin:$PATH' "$fragment"
check "launcher fragment declares the complete Java environment" $?

grep -Fq 'ENV JAVA_HOME=/opt/jbr' "$dockerfile" \
  && grep -Fq 'AllowEnhancedClassRedefinition' "$dockerfile" \
  && grep -Fq 'HotswapAgent=fatjar' "$dockerfile" \
  && grep -Fq 'ENV MAVEN_OPTS="-Dmaven.repo.local=\$HOME/.claude-contained/cache/maven"' "$dockerfile" \
  && grep -Fq 'ENV PATH="/opt/jbr/bin:/opt/maven/bin:/opt/jbang/bin:${PATH}"' "$dockerfile"
check "direct-image environment matches the launcher fragment contract" $?

direct_maven_opts="$(
  HOME=/Users/path-parity-dev \
    MAVEN_OPTS='-Dmaven.repo.local=$HOME/.claude-contained/cache/maven' \
    sh -c '. "$1"; printf "%s" "$MAVEN_OPTS"' sh "$mavenrc"
)"
if [[ "$direct_maven_opts" == '-Dmaven.repo.local=/Users/path-parity-dev/.claude-contained/cache/maven' ]]; then status=0; else status=1; fi
check "direct-image Maven state follows path-parity devcontainer HOME" "$status"

launcher_maven_opts="$(
  HOME=/Users/path-parity-launcher \
    MAVEN_OPTS='-Dmaven.repo.local=/Users/path-parity-launcher/.claude-contained/cache/maven' \
    sh -c '. "$1"; printf "%s" "$MAVEN_OPTS"' sh "$mavenrc"
)"
if [[ "$launcher_maven_opts" == '-Dmaven.repo.local=/Users/path-parity-launcher/.claude-contained/cache/maven' ]]; then status=0; else status=1; fi
check "Maven startup preserves launcher and attach resolved state" "$status"

fragment_options="$(sed -n 's/^JAVA_TOOL_OPTIONS=//p' "$fragment")"
image_options="$(sed -n 's/^ENV JAVA_TOOL_OPTIONS="\(.*\)"$/\1/p' "$dockerfile")"
if [[ -n "$fragment_options" && "$fragment_options" == "$image_options" ]]; then status=0; else status=1; fi
check "HotSwap options are identical across both startup seams" "$status"

grep -Fq '.claude-contained/cache/maven' "$dockerfile" "$fragment" "${example}/README.md" \
  && ! grep -Fq '.m2' "$dockerfile" "$fragment"
check "Maven state uses shared launcher state rather than a dedicated mount" $?

! grep -Eq 'INCLUDE_JAVA_LAYER|custom-packages|/opt/jbr|sdkman|JAVA_HOME|JAVA_TOOL_OPTIONS|MAVEN_OPTS' "${repo_root}/Dockerfile"
check "base Dockerfile contains no Java stage or package splice" $?

grep -Fq '"name": "Claude Contained"' "$devcontainer" \
  && grep -Fq '"image": "claude-contained:latest"' "$devcontainer" \
  && ! grep -Eqi 'java|spring|vaadin|lombok|\.m2|\.vaadin|/opt/jbr|8080|5005' "$devcontainer"
check "default devcontainer is plain and toolchain-neutral" $?

grep -Fq '"context": "../.claude-contained/layer/"' "${repo_root}/devcontainer/README.md" \
  && grep -Fq '"BASE_IMAGE": "claude-contained:latest"' "${repo_root}/devcontainer/README.md" \
  && grep -Fq 'examples/tooling-layers/java' "${repo_root}/devcontainer/README.md"
check "devcontainer guide documents a project tooling-layer build" $?

printf '%d passed, %d failed\n' "$passes" "$failures"
[[ "$failures" -eq 0 ]]

# Verify shipped tooling layers stay pinned to the repository's quality gate.
ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
REPO_LAYER="$ROOT_DIR/.claude-contained/layer"
EXAMPLE_LAYER="$ROOT_DIR/examples/tooling-layers/go"

assert_contains() {
  local file="$1" text="$2"
  if ! grep -Fq "$text" "$file"; then
    echo "missing from ${file#$ROOT_DIR/}: $text" >&2
    exit 1
  fi
}

assert_contains "$EXAMPLE_LAYER/Dockerfile" 'ARG GO_VERSION=1.24.3'
assert_contains "$REPO_LAYER/Dockerfile" 'ARG GO_VERSION=1.24.3'
assert_contains "$REPO_LAYER/Dockerfile" 'ARG SHELLCHECK_VERSION=0.11.0'
assert_contains "$REPO_LAYER/Dockerfile" 'ARG GOLANGCI_LINT_VERSION=2.12.2'
assert_contains "$REPO_LAYER/Dockerfile" 'sha256sum --check -'
assert_contains "$EXAMPLE_LAYER/Dockerfile" 'sha256sum --check -'

assert_contains "$ROOT_DIR/go.mod" 'go 1.24'
assert_contains "$ROOT_DIR/Makefile" 'SHELLCHECK_REQUIRED_VERSION := 0.11.0'
assert_contains "$ROOT_DIR/Makefile" 'GOLANGCI_LINT_REQUIRED_VERSION := 2.12.2'

cmp "$EXAMPLE_LAYER/10-go" "$REPO_LAYER/10-go"
assert_contains "$EXAMPLE_LAYER/10-go" 'GOTOOLCHAIN=local'
assert_contains "$EXAMPLE_LAYER/10-go" 'GOCACHE=${HOME}/.claude-contained/cache/go-build'
assert_contains "$EXAMPLE_LAYER/10-go" 'GOMODCACHE=${HOME}/.claude-contained/cache/go-mod'

echo "tooling layer assets: ok"
