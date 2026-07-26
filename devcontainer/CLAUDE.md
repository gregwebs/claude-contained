# devcontainer/

This directory is a template for VS Code devcontainer users, not an in-repo `.devcontainer/` (see `devcontainer/README.md` for usage).

**Key design decisions:**

- **Template directory, not in-repo `.devcontainer/`**: Users copy to their own projects; avoids confusion with developing claude-contained itself
- **`workspaceMount: ""`**: Disables VS Code's default `/workspaces` mount to enable path parity
- **`overrideCommand: true`**: Bypasses entrypoint.sh since VS Code manages container lifecycle
- **Pre-built image reference**: Simpler than embedding Dockerfile; users build once, reuse everywhere

**Differences from standalone scripts:**

- VS Code manages the container lifecycle, not entrypoint.sh
- UID/GID handled by VS Code's `remoteUser` feature (may differ from host)
- Networking managed by VS Code; `host.local` trick may not work
