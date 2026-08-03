ARG BASE_IMAGE=claude-contained:latest
FROM ${BASE_IMAGE}

ARG GO_VERSION=1.24.3
ARG GO_AMD64_SHA256=3333f6ea53afa971e9078895eaa4ac7204a8c6b5c68c10e6bc9a33e8e391bdd8
ARG GO_ARM64_SHA256=a463cb59382bd7ae7d8f4c68846e73c4d589f223c589ac76871b66811ded7836

RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) checksum="$GO_AMD64_SHA256" ;; \
      arm64) checksum="$GO_ARM64_SHA256" ;; \
      *) echo "Unsupported architecture: $arch" >&2; exit 1 ;; \
    esac; \
    archive="go${GO_VERSION}.linux-${arch}.tar.gz"; \
    curl -fsSL "https://go.dev/dl/${archive}" -o "/tmp/${archive}"; \
    echo "${checksum}  /tmp/${archive}" | sha256sum --check -; \
    tar -C /usr/local -xzf "/tmp/${archive}"; \
    rm "/tmp/${archive}"; \
    ln -s /usr/local/go/bin/go /usr/local/bin/go; \
    ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt; \
    go version

COPY 10-go /etc/claude-contained/env.d/10-go
