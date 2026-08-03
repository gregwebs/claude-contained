# Tooling-layer examples

Each subdirectory is a complete tooling-layer build context. Copy one into a
project's `.claude-contained/layer/` directory, then adjust its pinned versions
and checksums for that project.

```mermaid
flowchart LR
    P[Project tooling layer] -->|confirmed build| D[Derived image]
    D -->|starts with fragments| T[Contained tool process]
    T -->|module and build caches| S[Host-backed shared state]
```

- [Go](go/) installs a checksum-verified Go toolchain, persists its caches, and
  prevents automatic toolchain downloads.
- [Java](java/README.md) provides a JDK, Maven, JBang, HotswapAgent, and JDT LS.

Read [Tooling Layers](../../USAGE.md#tooling-layers) for the build confirmation,
derived-image identity, environment-fragment, and cleanup behavior.
