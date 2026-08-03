# Java tooling layer

This complete tooling-layer build context adds JetBrains Runtime/JDK 25.0.1
build b268.52, HotswapAgent 2.0.3, Eclipse JDT LS 1.40.0, Maven 3.9.11,
and JBang 0.141.0. Update the version arguments together with their download
URLs; each published checksum is verified before extraction.

Copy this directory into a project and build it on first use:

```bash
mkdir -p .claude-contained
cp -R /path/to/claude-contained/examples/tooling-layers/java .claude-contained/layer
claude-contained --build-layer
```

The image links `java`, `javac`, `jar`, `mvn`, `jbang`, and `jdtls` into
`/usr/local/bin`. They therefore survive a login shell resetting `PATH`, and
the installed `10-java` layer env fragment makes the same environment available
to launcher runs and attach sessions.

The Dockerfile also declares matching image `ENV` values because VS Code
devcontainers bypass both the launcher and its entrypoint resolver. Its Maven
value deliberately retains a literal `$HOME`; the installed Maven startup file
resolves that template after a path-parity devcontainer supplies its effective
home. Launcher and attach runs instead receive the already-resolved value from
`10-java`. Keep these startup declarations aligned; the asset test enforces
their shared contract.

Maven writes to `$HOME/.claude-contained/cache/maven`. That directory persists
through the launcher's existing shared-state mount across replacement
containers. The host's `~/.m2` is separate and is not mounted automatically.

The sandbox still denies network hosts that are not allowed. Add the artifact
repositories a build needs with `--allow-host`; Maven Central commonly requires
`repo.maven.apache.org`.
