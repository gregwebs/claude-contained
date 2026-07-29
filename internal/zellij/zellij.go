// Package zellij is the launcher-side half of the Zellij session store: which
// session a run targets, which running containers are backing one, whether a
// new launch is allowed, and the two commands the container ever runs for
// Zellij.
//
// The other half lives in the image (image/zellij-run.sh, image/zellij-attach.sh,
// image/srt-settings.sh) and stays shell by ADR-0004. This package's job is to
// hand those scripts exactly the inputs they expect: a session name that
// matches what bash produced, and both environment markers, always together.
//
// No container-runtime *command* appears here: internal/runtime renders the
// ExecSpec and decides whether labels exist at all. The one runtime-derived
// value this package takes is the launcher's own program name, passed in for
// the rebuild hint in runGuardFmt -- a string bash likewise interpolates
// (claude-contained:1970 vs claude-docked:1972), not a command.
package zellij

import (
	"fmt"
	"io"
	"strings"

	"claude-contained/internal/attach"
	"claude-contained/internal/host"
	"claude-contained/internal/runtime"
)

const (
	// MarkerEnv/SessionEnv mark a container as backing a Zellij session
	// (claude-contained:1818-1822). Environment inspection is the portable
	// source of truth: Apple Containers has no labels, so discovery that read
	// labels would work on one runtime only (ADR-0002).
	MarkerEnv  = "CLAUDE_CONTAINED_ZELLIJ"
	SessionEnv = "CLAUDE_CONTAINED_ZELLIJ_SESSION"

	// LabelMarker/LabelSession are recorded by Docker (claude-docked:1805-1806)
	// and deliberately never read. Emitted for external tooling only.
	LabelMarker  = "claude-contained.zellij"
	LabelSession = "claude-contained.zellij.session"

	// namePrefix is the generated-session-name prefix (claude-contained:373).
	namePrefix = "cc-"

	// runHelper is the in-container wrapper that becomes the top-level
	// container process (claude-contained:1974).
	runHelper = "zellij-run"
	// attachHelper reconnects a client to a live session (claude-contained:172).
	attachHelper = "/usr/local/bin/zellij-attach"
	// sandboxWrapper is srt-run; `exec` bypasses the entrypoint, so the attach
	// path re-applies it (claude-contained:171).
	sandboxWrapper = "srt-run"
	// ShellCommand is what --shell runs *under Zellij*: plain bash, not
	// shell-run (claude-contained:1957-1961).
	ShellCommand = "bash"
)

// SessionName mirrors default_zellij_session_name (claude-contained:370-374).
// Both halves must be byte-identical to bash's or users lose access to the
// sessions they already have. host.PathHash8 is bash's path_hash_8
// (claude-contained:358-368): the first 8 lowercase hex characters of the
// SHA-256 of the path's bytes, with no trailing newline.
func SessionName(projectDir string) string {
	return namePrefix + host.SanitizeFolderName(projectDir) + "-" + host.PathHash8(projectDir)
}

// SessionFromEnv mirrors zellij_session_from_env_lines
// (claude-contained:430-441): the marker match is exact equality on
// "CLAUDE_CONTAINED_ZELLIJ=1"; the session match is a prefix on
// "CLAUDE_CONTAINED_ZELLIJ_SESSION=", with the last occurrence winning. Both
// conditions are required -- either alone yields "".
func SessionFromEnv(lines []string) string {
	marker := false
	session := ""
	for _, line := range lines {
		switch {
		case line == MarkerEnv+"=1":
			marker = true
		case strings.HasPrefix(line, SessionEnv+"="):
			session = strings.TrimPrefix(line, SessionEnv+"=")
		}
	}
	if marker && session != "" {
		return session
	}
	return ""
}

// Record is one live Zellij-backed container, in runtime list order.
type Record struct {
	Container string
	Session   string
}

// ResolveLaunch mirrors the launch gate (claude-contained:1439-1471). It
// returns 0 to proceed, 1 to refuse (after writing its own message). The
// target-live check runs first and is not overridden by newSession -- only
// the second check, "any Zellij container is already live", consults it.
func ResolveLaunch(session string, records []Record, newSession bool, stderr io.Writer) int {
	for _, r := range records {
		if r.Session == session {
			_, _ = fmt.Fprintf(stderr, "Zellij session '%s' is already live.\n", session)
			_, _ = fmt.Fprintf(stderr, "Use --zellij --attach --session %s, or choose a different session with --session=NAME.\n", session)
			return 1
		}
	}
	if !newSession && len(records) >= 1 {
		_, _ = fmt.Fprintln(stderr, "A Zellij-backed container is already running:")
		for _, r := range records {
			_, _ = fmt.Fprintf(stderr, "  %s\n", r.Session)
		}
		_, _ = fmt.Fprintln(stderr, "Use --zellij --attach [--session NAME] to reconnect, or --zellij --new-session [--session NAME] to start another session.")
		return 1
	}
	return 0
}

// AttachRequest is everything ResolveAttach needs.
type AttachRequest struct {
	Session    string
	SrtDisable bool
	Home       string
	Records    []Record

	Stdout, Stderr io.Writer
	// Prompt writes the prompt and returns one raw input line. ok=false is
	// EOF.
	Prompt func(prompt string) (line string, ok bool)
}

// ResolveAttach mirrors the Zellij attach block (claude-contained:896-950).
// It never creates a container: attach.Decision has no way to express "run a
// container" -- every non-exec outcome here is an exit.
func ResolveAttach(req AttachRequest) attach.Decision {
	if req.Session == "" {
		return resolveAttachInteractive(req)
	}
	return resolveAttachByName(req)
}

func resolveAttachInteractive(req AttachRequest) attach.Decision {
	if len(req.Records) == 0 {
		_, _ = fmt.Fprintln(req.Stdout, "No live Zellij sessions")
		return attach.Decision{Code: 0}
	}
	if len(req.Records) == 1 {
		return attach.Decision{Spec: buildAttachSpec(req, req.Records[0])}
	}

	_, _ = fmt.Fprintln(req.Stdout, "Live Zellij sessions:")
	for i, r := range req.Records {
		_, _ = fmt.Fprintf(req.Stdout, "  %d) %s (%s)\n", i+1, r.Session, attach.DisplayName(r.Container))
	}
	_, _ = fmt.Fprintln(req.Stdout, "")

	if req.Prompt == nil {
		// No prompt to read from is the same as an immediate EOF
		// (internal/attach/attach.go:143-146).
		return attach.Decision{Code: 1}
	}
	line, ok := req.Prompt(fmt.Sprintf("Select session (1-%d, q to quit): ", len(req.Records)))
	if !ok {
		return attach.Decision{Code: 1}
	}

	idx, quit, valid := attach.ParseSelection(line, len(req.Records))
	if quit {
		return attach.Decision{Code: 0}
	}
	if !valid {
		_, _ = fmt.Fprintln(req.Stderr, "Invalid selection")
		return attach.Decision{Code: 1}
	}
	return attach.Decision{Spec: buildAttachSpec(req, req.Records[idx-1])}
}

func resolveAttachByName(req AttachRequest) attach.Decision {
	var matches []Record
	for _, r := range req.Records {
		if r.Session == req.Session {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		_, _ = fmt.Fprintf(req.Stderr, "No live Zellij session named %s\n", req.Session)
		return attach.Decision{Code: 1}
	case 1:
		return attach.Decision{Spec: buildAttachSpec(req, matches[0])}
	default:
		_, _ = fmt.Fprintf(req.Stderr, "Multiple live containers report Zellij session %s; refusing ambiguous attach\n", req.Session)
		return attach.Decision{Code: 1}
	}
}

// buildAttachSpec assembles the ExecSpec for a resolved live record. The
// Zellij attach exec carries no user --env pairs (§3.5), unlike the plain
// attach execs, which is why ExecEnv is called with a nil pairs slice.
func buildAttachSpec(req AttachRequest, rec Record) *runtime.ExecSpec {
	return &runtime.ExecSpec{
		Container: rec.Container,
		User:      "dev",
		TTY:       true,
		Env:       attach.ExecEnv(req.Home, nil),
		Command:   AttachCommand(rec.Session, req.SrtDisable),
	}
}

// AttachCommand mirrors build_zellij_attach_cmd (claude-contained:167-173).
func AttachCommand(session string, srtDisable bool) []string {
	var cmd []string
	if !srtDisable {
		cmd = append(cmd, sandboxWrapper)
	}
	return append(cmd, attachHelper, session)
}

// runGuardFmt is the script bash passes to `bash -lc` (claude-contained:1968-1973).
// The indentation is part of the argv element and is reproduced exactly; the
// differential harness compares the runtime argv byte for byte. %s is the
// launcher's program name, the only token that differs between the two
// launchers (claude-contained:1970 vs claude-docked:1972).
const runGuardFmt = `if ! command -v "$0" >/dev/null 2>&1; then
       echo "error: claude-contained image is missing Zellij support (zellij-run)." >&2
       echo "       Rebuild it with: %s --rebuild=full" >&2
       exit 127
     fi
     exec "$0" "$@"`

// RunCommand mirrors the container command wrapper (claude-contained:1954-1976).
func RunCommand(session, progName string, inner []string) []string {
	argv := []string{"bash", "-lc", fmt.Sprintf(runGuardFmt, progName), runHelper, session, "--"}
	return append(argv, inner...)
}
