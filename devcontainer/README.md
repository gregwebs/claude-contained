# Devcontainer template

This template runs the plain `claude-contained:latest` base image with path
parity, contained Claude state, shared extension resources, and Claude available
in the integrated terminal. It deliberately supplies no project toolchain.

## Setup

Build the base image, create the shared host directories, and copy the template:

```bash
docker build -t claude-contained /path/to/claude-contained
mkdir -p ~/.claude-contained/claude ~/.claude/{skills,agents,commands,plugins}
cp -R /path/to/claude-contained/devcontainer /path/to/project/.devcontainer
```

Open the project in VS Code and choose **Dev Containers: Reopen in Container**.
Do not run this devcontainer and a standalone launcher simultaneously against
the same contained Claude profile.

## Use a project's tooling layer

Copy an example such as
[`examples/tooling-layers/java`](../examples/tooling-layers/java/README.md) into
the project first. Then replace the top-level `image` property in
`devcontainer.json` with:

```json
"build": {
  "context": "../.claude-contained/layer/",
  "args": {
    "BASE_IMAGE": "claude-contained:latest"
  }
}
```

The path is relative to `.devcontainer/devcontainer.json`. This direct derived-
image build does not use the launcher, so it intentionally skips the launcher's
tooling-layer confirmation and content-hashed tag. A tooling layer used this way
must declare its ordinary runtime environment with Dockerfile `ENV`; the Java
example does so in addition to its launcher env fragment.

## Customize the project

Add project-specific extensions, forwarded ports, and extra mounts to the copied
file. VS Code manages networking and the `dev` user's lifecycle; host services
may need explicit port forwarding. UID/GID handling can also differ from the
standalone launcher.
