ARG BASE_IMAGE=claude-contained:latest
FROM ${BASE_IMAGE}

ARG GO_VERSION=1.24.3
ARG GO_AMD64_SHA256=3333f6ea53afa971e9078895eaa4ac7204a8c6b5c68c10e6bc9a33e8e391bdd8
ARG GO_ARM64_SHA256=a463cb59382bd7ae7d8f4c68846e73c4d589f223c589ac76871b66811ded7836
ARG SHELLCHECK_VERSION=0.11.0
ARG SHELLCHECK_AMD64_SHA256=8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198
ARG SHELLCHECK_ARM64_SHA256=12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588
ARG GOLANGCI_LINT_VERSION=2.12.2
ARG GOLANGCI_LINT_AMD64_SHA256=8df580d2670fed8fa984aac0507099af8df275e665215f5c7a2ae3943893a553
ARG GOLANGCI_LINT_ARM64_SHA256=44cd40a8c76c86755375adfeea52cfd3533cb43d7bd647771e0ae065e166df3a

# These downloads mirror .github/workflows/quality.yml. The arm64 variants
# make the same quality gate available under Apple Containers.
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) \
        go_checksum="$GO_AMD64_SHA256"; \
        shellcheck_arch=x86_64; \
        shellcheck_checksum="$SHELLCHECK_AMD64_SHA256"; \
        golangci_checksum="$GOLANGCI_LINT_AMD64_SHA256" \
        ;; \
      arm64) \
        go_checksum="$GO_ARM64_SHA256"; \
        shellcheck_arch=aarch64; \
        shellcheck_checksum="$SHELLCHECK_ARM64_SHA256"; \
        golangci_checksum="$GOLANGCI_LINT_ARM64_SHA256" \
        ;; \
      *) echo "Unsupported architecture: $arch" >&2; exit 1 ;; \
    esac; \
    go_archive="go${GO_VERSION}.linux-${arch}.tar.gz"; \
    curl -fsSL "https://go.dev/dl/${go_archive}" -o "/tmp/${go_archive}"; \
    echo "${go_checksum}  /tmp/${go_archive}" | sha256sum --check -; \
    tar -C /usr/local -xzf "/tmp/${go_archive}"; \
    rm "/tmp/${go_archive}"; \
    ln -s /usr/local/go/bin/go /usr/local/bin/go; \
    ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt; \
    shellcheck_archive="shellcheck-v${SHELLCHECK_VERSION}.linux.${shellcheck_arch}.tar.xz"; \
    curl -fsSL "https://github.com/koalaman/shellcheck/releases/download/v${SHELLCHECK_VERSION}/${shellcheck_archive}" -o "/tmp/${shellcheck_archive}"; \
    echo "${shellcheck_checksum}  /tmp/${shellcheck_archive}" | sha256sum --check -; \
    tar -xJf "/tmp/${shellcheck_archive}" -C /tmp; \
    install -m 0755 "/tmp/shellcheck-v${SHELLCHECK_VERSION}/shellcheck" /usr/local/bin/shellcheck; \
    rm -rf "/tmp/${shellcheck_archive}" "/tmp/shellcheck-v${SHELLCHECK_VERSION}"; \
    golangci_archive="golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${arch}.tar.gz"; \
    curl -fsSL "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/${golangci_archive}" -o "/tmp/${golangci_archive}"; \
    echo "${golangci_checksum}  /tmp/${golangci_archive}" | sha256sum --check -; \
    tar -xzf "/tmp/${golangci_archive}" -C /tmp; \
    install -m 0755 "/tmp/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${arch}/golangci-lint" /usr/local/bin/golangci-lint; \
    rm -rf "/tmp/${golangci_archive}" "/tmp/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-${arch}"; \
    go version; \
    shellcheck --version; \
    golangci-lint version

COPY 10-go /etc/claude-contained/env.d/10-go
