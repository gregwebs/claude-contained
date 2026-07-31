package runtime

import goruntime "runtime"

// Platform is the host operating system, as a value rather than a read of
// runtime.GOOS at each use site.
//
// GOOS is a compile-time constant for a whole test binary, so inline reads make
// the Linux arms unreachable from a macOS developer machine and the macOS arms
// unreachable from CI, which runs `make quality` on ubuntu. Injecting it is what
// makes all three supported configurations testable on either host. See
// docs/adr/0004-go-launcher-rewrite.md for the full rationale, including why
// this is threaded through the constructors rather than held in a package
// variable.
//
// The zero value is an unnamed platform, and every branch treats it exactly as
// the bash launchers' else-arms do: not Darwin, so Docker mounts the real agent
// socket (claude-docked:1819); not Linux, so there is no host-gateway mapping
// (claude-docked:1828); and the default runtime is Docker, because Apple
// Containers exists only on macOS.
type Platform string

const (
	Darwin Platform = "darwin"
	Linux  Platform = "linux"
)

// HostPlatform reports the platform this binary runs on. This is the only
// production read of runtime.GOOS inside the seam.
//
// cmd/claude-contained/probe.go reads GOOS too, and deliberately keeps its own read: it
// answers a different question -- "is the host already Linux, so that the
// cross-platform node_modules overlay is pointless" -- which is about the
// container *image*, not about the container runtime. Do not unify them.
func HostPlatform() Platform { return Platform(goruntime.GOOS) }
