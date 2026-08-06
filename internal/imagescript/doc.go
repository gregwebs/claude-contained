// Package imagescript is the home of the image-side shell-script test suite.
//
// The launcher is Go, but the container image still ships small shell scripts
// under image/ that run inside the image where bash is a given: port
// forwarding, the srt sandbox-policy generator, the tool-environment resolver,
// the debug-shell PTY wrapper, the Claude native-link creator, and the Zellij
// wrappers. These scripts have no owning Go package, so -- exactly as the
// compiled-binary black-box suite lives in cmd/claude-contained -- their tests
// live here, in a dedicated package.
//
// Every test in this package (all in *_test.go) runs a real image/*.sh under
// bash and asserts its observable contract structurally: argv of the external
// commands it drives, the files and permissions it produces, the JSON policy it
// generates, and its fail-closed behavior on bad input. External commands whose
// argument boundary -- not their implementation -- is under test (socat,
// script, zellij, id) are modeled by the reusable re-exec stub in
// internal/blackbox. Bash and jq are mandatory contributor prerequisites that
// fail the test clearly when absent; they are never skipped. See ADR-0008.
//
// This is a normal (non-_test.go) file only so the package is documented; it
// imports nothing and never links into a build artifact.
package imagescript
