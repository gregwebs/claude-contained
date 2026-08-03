# Go tooling layer

This example installs Go 1.24.3 for Linux `amd64` or `arm64` from the official
archive and verifies its SHA-256 checksum before extracting it. `go` and
`gofmt` are linked into `/usr/local/bin`, so tools such as Codex that start a
login shell and reset `PATH` can still find them.

Copy this directory to `.claude-contained/layer/` in a Go project and update
the pinned version and both checksums together. The `10-go` fragment puts the
module and build caches under the host-backed `~/.claude-contained/` directory,
so they survive container replacement. `GOTOOLCHAIN=local` prevents Go from
silently downloading a different toolchain when `go.mod` requires one.

The sandbox denies network access unless a host is allowed. The default Go
module proxy and checksum database need:

```bash
claude-contained \
  --allow-host proxy.golang.org \
  --allow-host sum.golang.org \
  --allow-host storage.googleapis.com
```

Add the source host too when a module is fetched directly (for example,
`--allow-host github.com`). These flags affect tool runtime traffic; the
confirmed tooling-layer build itself runs with unrestricted network access.
