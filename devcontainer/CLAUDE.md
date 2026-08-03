# devcontainer/

This directory is a generic template that users copy into a project; it is not
this repository's own `.devcontainer/`.

- `workspaceMount: ""` preserves the project's host path.
- `overrideCommand: true` lets VS Code manage the container lifecycle and means
  the image entrypoint and layer env resolver do not run.
- The default references the plain base image. Projects that need a toolchain
  replace `image` with the documented build of their copied tooling layer.
- A tooling layer intended for direct devcontainer use must provide image-level
  environment in addition to any launcher env fragment.
