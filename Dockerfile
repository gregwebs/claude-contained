# Claude Code and companion coding agents in a toolchain-neutral base image.
FROM node:24-bookworm-slim AS base

# ---- System packages --------------------------------------------------------
RUN set -eux; \
    BASE_PACKAGES=" \
      git make openssh-client ca-certificates ripgrep jq \
      curl bash xz-utils unzip tzdata \
      python3 python3-pip python3-venv \
      iproute2 gosu socat util-linux \
      libasound2 libatk-bridge2.0-0 libatk1.0-0 libatspi2.0-0 \
      libcups2 libdbus-1-3 libdrm2 libgbm1 libgtk-3-0 \
      libnspr4 libnss3 libpango-1.0-0 libxcomposite1 libxdamage1 \
      libxfixes3 libxkbcommon0 libxrandr2 libxtst6 xvfb zip unzip bubblewrap"; \
    apt-get update && apt-get install -y --no-install-recommends \
      $BASE_PACKAGES \
    && rm -rf /var/lib/apt/lists/*

# ---- Install git-secrets ----------------------------------------------------
ARG GIT_SECRETS_VERSION=1.3.0
RUN set -eux; \
    curl -fL "https://github.com/awslabs/git-secrets/archive/refs/tags/${GIT_SECRETS_VERSION}.tar.gz" -o /tmp/git-secrets.tar.gz; \
    mkdir -p /tmp/git-secrets; \
    tar -xzf /tmp/git-secrets.tar.gz -C /tmp/git-secrets --strip-components=1; \
    make -C /tmp/git-secrets install PREFIX=/usr/local; \
    rm -rf /tmp/git-secrets /tmp/git-secrets.tar.gz; \
    git secrets --scan /dev/null

# ---- Install Bun ------------------------------------------------------------
ARG BUN_VERSION=latest
RUN set -eux; \
    ARCH="$(dpkg --print-architecture)"; \
    case "$ARCH" in \
      arm64)  BUN_ARCH="aarch64" ;; \
      amd64)  BUN_ARCH="x64" ;; \
      *)      echo "Unsupported architecture: $ARCH"; exit 1 ;; \
    esac; \
    if [ "$BUN_VERSION" = "latest" ]; then \
      BUN_VERSION=$(curl -fsSL https://api.github.com/repos/oven-sh/bun/releases/latest | grep -oP '"tag_name": "bun-v\K[^"]+'); \
    fi; \
    echo "Installing Bun v${BUN_VERSION}"; \
    URL="https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-${BUN_ARCH}.zip"; \
    curl -fL "$URL" -o /tmp/bun.zip; \
    unzip -q /tmp/bun.zip -d /tmp; \
    mv /tmp/bun-linux-${BUN_ARCH}/bun /usr/local/bin/bun; \
    chmod +x /usr/local/bin/bun; \
    rm -rf /tmp/bun.zip /tmp/bun-linux-${BUN_ARCH}; \
    bun --version

# ---- Zellij terminal workspace ---------------------------------------------
ARG ZELLIJ_VERSION=0.44.3
RUN set -eux; \
    ARCH="$(dpkg --print-architecture)"; \
    case "$ARCH" in \
      arm64)  ZELLIJ_TARGET="aarch64-unknown-linux-musl" ;; \
      amd64)  ZELLIJ_TARGET="x86_64-unknown-linux-musl" ;; \
      *)      echo "Unsupported architecture: $ARCH"; exit 1 ;; \
    esac; \
    URL="https://github.com/zellij-org/zellij/releases/download/v${ZELLIJ_VERSION}/zellij-${ZELLIJ_TARGET}.tar.gz"; \
    curl -fL "$URL" -o /tmp/zellij.tar.gz; \
    tar -xzf /tmp/zellij.tar.gz -C /usr/local/bin zellij; \
    chmod +x /usr/local/bin/zellij; \
    rm -f /tmp/zellij.tar.gz; \
    zellij --version

# ---- Language Servers + AI CLIs --------------------------------------------
ARG AI_TOOLS_CACHE_BUST=stable
RUN set -eux; \
    echo "Refreshing AI tool layers: ${AI_TOOLS_CACHE_BUST}" >/dev/null; \
    npm install -g \
    @anthropic-ai/sandbox-runtime \
    @github/copilot \
    @google/gemini-cli \
    @openai/codex \
    typescript \
    typescript-language-server \
    pyright \
  && npm cache clean --force

# ---- Native Claude Code binary ----------------------------------------------
# Download native binary to /opt/claude/ (runtime creates user symlinks)
ARG CLAUDE_VERSION=latest
RUN set -eux; \
    ARCH="$(dpkg --print-architecture)"; \
    case "$ARCH" in \
      arm64)  CLAUDE_PLATFORM="linux-arm64" ;; \
      amd64)  CLAUDE_PLATFORM="linux-x64" ;; \
      *)      echo "Unsupported architecture: $ARCH"; exit 1 ;; \
    esac; \
    GCS_BUCKET="https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases"; \
    if [ "$CLAUDE_VERSION" = "latest" ]; then \
      CLAUDE_VERSION=$(curl -fsSL "$GCS_BUCKET/latest"); \
    fi; \
    mkdir -p /opt/claude; \
    curl -fsSL "$GCS_BUCKET/$CLAUDE_VERSION/$CLAUDE_PLATFORM/claude" -o /opt/claude/claude; \
    chmod +x /opt/claude/claude; \
    /opt/claude/claude --version

# ---- Mistral Vibe (requires Python 3.12+, use uv for version management) ---
ENV UV_TOOL_BIN_DIR=/usr/local/bin
ENV UV_TOOL_DIR=/opt/uv-tools
ENV UV_PYTHON_INSTALL_DIR=/opt/uv-python
RUN curl -LsSf https://astral.sh/uv/install.sh | sh \
  && /root/.local/bin/uv tool install mistral-vibe --python 3.12 \
  && chmod -R a+rX /opt/uv-tools /opt/uv-python

# ---- Playwright browser (build-time install for reliability) ----------------
# Install Chromium to a fixed location instead of user cache for container use
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
RUN npx playwright@1.61.0 install --with-deps chromium chromium-headless-shell

# ---- Chrome wrapper for Playwright MCP compatibility ------------------------
# When projects use @playwright/mcp without --browser flag, it looks for Chrome.
# This wrapper redirects to our installed Playwright Chromium.
COPY image/chrome-wrapper.sh /opt/google/chrome/chrome
RUN chmod +x /opt/google/chrome/chrome

# ---- Non-root user ----------------------------------------------------------
RUN useradd -m -s /bin/bash dev \
  && mkdir -p /work \
  && chown -R dev:dev /work /home/dev /ms-playwright

# ---- Claude Code clipboard workaround --------------------------------------
# Force the classic inline TUI renderer ("tui": "default") inside the container.
# The newer fullscreen ("no-flicker") renderer (default since ~2.1.168) routes
# copy-on-select only through OSC 52 and captures the mouse. Inside a container
# attached to the host terminal there is no clipboard tool (no pbcopy/display),
# OSC 52 is silently dropped by terminals like Terminal.app, and the mouse
# capture also breaks native shift/option-drag selection -- so copying from
# Claude stops working entirely. See anthropics/claude-code#66192.
#
# This is a managed-settings file (highest precedence, Linux path), so it is
# container-scoped and never touches the host-mounted ~/.claude/settings.json.
# Remove this once the upstream renderer regression is fixed.
RUN mkdir -p /etc/claude-code \
    && printf '%s\n' '{ "tui": "default" }' > /etc/claude-code/managed-settings.json

# ---- Sandbox (srt) helpers --------------------------------------------------
# srt's Linux dependencies -- bubblewrap, socat, ripgrep -- are already installed
# with the base packages above. srt-settings.sh generates the per-run policy;
# srt-run wraps a command for `container exec`, which bypasses the entrypoint.
# shell-run starts debug shells through a child PTY when needed.
COPY image/srt-settings.sh /usr/local/bin/srt-settings.sh
COPY image/srt-run.sh /usr/local/bin/srt-run
COPY image/shell-run.sh /usr/local/bin/shell-run
COPY image/claude-native-link.sh /usr/local/bin/claude-native-link
COPY image/host-forward.sh /usr/local/bin/host-forward
COPY image/zellij/ /etc/claude-contained/zellij/
COPY image/zellij-run.sh /usr/local/bin/zellij-run
COPY image/zellij-attach.sh /usr/local/bin/zellij-attach
COPY image/zellij-pane-command.sh /usr/local/bin/zellij-pane-command
RUN chmod +x /usr/local/bin/srt-settings.sh /usr/local/bin/srt-run \
    /usr/local/bin/shell-run \
    /usr/local/bin/claude-native-link /usr/local/bin/host-forward \
    /usr/local/bin/zellij-run /usr/local/bin/zellij-attach \
    /usr/local/bin/zellij-pane-command

# ---- Entrypoint (host.local setup + path parity) ---------------------------
COPY image/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY image/tool-env.sh /usr/local/bin/tool-env
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/tool-env

WORKDIR /work
# HOME is set dynamically in entrypoint based on HOST_HOME

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["claude"]
