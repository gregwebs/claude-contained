// Package cli parses the launcher's flag-only command line into a Config.
//
// The whole flag surface is parsed here even though ticket 02 only executes the
// basic run path. Splitting the parser would mean porting it twice, and the
// flag-only CLI is one unit: `--` handling, the require_value rule and the
// unknown-flag arm all interact.
package cli

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"claude-contained/internal/host"
)

// Exit codes. 0/1/2 mirror bash. Status 3 is no longer produced by anything:
// ticket 10 removed the last CheckUnportedEarly guard (-R/--rebuild), so
// nothing above this package should reuse it for a new meaning.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// ExitError carries the process exit code for a failure that has already
// reported itself to stderr. Returning it rather than exiting keeps every exit
// funnelled through main, which is what lets cleanup run on every path.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit %d", e.Code) }

func exitWith(code int) error { return &ExitError{Code: code} }

// RuntimeFlag selects the container runtime. It is the first flag the bash
// launchers do not have -- they select their runtime by *being a different file*
// -- which makes it a deliberate divergence in the unknown-flag path rather than
// an oversight. See docs/adr/0004-go-launcher-rewrite.md; ticket 11 drops the
// second launcher name and leaves this as the only way for Docker users to
// choose.
const RuntimeFlag = "--container-runtime"

// BuildContextFlag names the checkout --rebuild builds from. Like RuntimeFlag it
// is a flag the bash launchers do not have -- they are scripts inside the
// checkout, so they always find it by self-location -- which makes it a
// deliberate divergence in the unknown-flag path, pinned by
// tests/arg-parsing.test.sh rather than left accidental.
const BuildContextFlag = "--build-context"

// valueTakingFlags are the flags that consume the *following* token as their
// value. ScanRuntime skips those tokens, so that `-e --container-runtime=docker`
// is an environment assignment (which Parse then rejects) rather than a runtime
// selection. Without the skip, the pre-scan and Parse would disagree, and Parse's
// rejection would name the wrong program in its "run '%s --help'" line.
//
// -R and -a are absent deliberately: their values are optional and are only
// consumed when the next token does not start with a dash, which
// --container-runtime always does.
var valueTakingFlags = map[string]bool{
	"-C": true, "--dir": true,
	"-m": true, "--mount": true,
	"--name":         true,
	"--session":      true,
	"--share-skills": true,
	"-t":             true,
	"--tool":         true,
	"-e":             true,
	"--env":          true,
	"-p":             true,
	"-H":             true,
	"--dns":          true,
	"--allow-host":   true,
	RuntimeFlag:      true,
	BuildContextFlag: true,
}

// ScanRuntime extracts the container-runtime flag before Parse runs.
//
// Two scans of argv exist because the runtime has to be chosen *before* the
// command line is parsed -- Parse's error messages embed the program name and
// --help prints the selected runtime's own literal text -- while validation of
// the value belongs after parsing, so that -h/--help still wins. Selection uses
// this scan; the diagnosis uses Config.ContainerRuntime from the real parse.
// TestScanRuntimeAgreesWithParse pins them together.
//
// Last occurrence wins, matching Parse. A missing or dash-leading value yields
// "" and leaves the report to Parse. Nothing after `--` is examined, so a tool
// argument can never select a runtime.
func ScanRuntime(args []string) string {
	value := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return value
		case arg == RuntimeFlag:
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				value = args[i+1]
				i++
			}
		case strings.HasPrefix(arg, RuntimeFlag+"="):
			value = strings.TrimPrefix(arg, RuntimeFlag+"=")
		case valueTakingFlags[arg]:
			// Skip unconditionally, even when the next token looks like a flag:
			// Parse consumes it as a value or rejects the whole command line, and
			// either way it is not a runtime selection.
			i++
		}
	}
	return value
}

// Config is the parsed command line.
type Config struct {
	ShellMode            bool
	SSHMode              bool
	WorktreeMode         bool
	LockWorktrees        bool
	YoloMode             bool
	ContainedNodeModules bool
	AttachMode           bool
	AttachName           string
	CustomContainerName  string
	ProjectDir           string
	ExtraMounts          []string
	ToolArgs             []string
	// ContainerRuntime is --container-runtime's value, unvalidated: the accepted
	// names live in internal/runtime, which diagnoses a bad one after --help has
	// had its chance.
	ContainerRuntime string
	// BuildContext is --build-context's value, unvalidated: internal/host checks
	// for a Dockerfile when a rebuild actually needs one.
	BuildContext         string
	ZellijMode           bool
	ZellijNewSession     bool
	ZellijSessionName    string
	ZellijSessionNameSet bool
	RebuildMode          string
	Tool                 string
	ReadonlyExtras       bool
	ShareSkillsDir       string
	ShareHostClaude      bool
	PortMaps             []string
	HostForwards         []string
	DNSServers           []string
	SrtAllowHosts        []string
	SrtDisable           bool
	EnvFlagArgs          []string
	NoProjectEnv         bool
	// HelpRequested short-circuits everything else.
	HelpRequested bool
}

// Parse mirrors claude-contained:604-878: the flag loop, then the post-parse
// validation block, in that order. Messages are written verbatim, because the
// golden tests compare stderr byte for byte.
//
// Bash also has a pre-loop shortcut for a leading -h/--help (:553-556). It is
// not reproduced because it is unobservable: the loop's own -h/--help arm
// handles the same input identically, and anything earlier in argv already
// short-circuits in both.
//
// progName is the runtime-specific program name; it appears only in the two
// error arms that embed it. shareHostClaudeEnv carries
// CLAUDE_CONTAINED_SHARE_HOST_CLAUDE.
func Parse(args []string, progName string, shareHostClaudeEnv bool, stderr io.Writer) (Config, error) {
	cfg := Config{
		RebuildMode:     "none",
		Tool:            "claude",
		ShareHostClaude: shareHostClaudeEnv,
	}

	requireValue := func(flag, value, what string) error {
		if value == "" || strings.HasPrefix(value, "-") {
			if what == "" {
				what = "a value"
			}
			_, _ = fmt.Fprintf(stderr, "error: %s requires %s\n", flag, what)
			return exitWith(ExitUsage)
		}
		return nil
	}

	// requireInline covers the `--flag=` forms that carry their own empty check.
	requireInline := func(flag, value, what string) error {
		if value == "" {
			_, _ = fmt.Fprintf(stderr, "error: %s requires %s\n", flag, what)
			return exitWith(ExitUsage)
		}
		return nil
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// `--` matches -* and must be handled before any flag dispatch, or the
		// unknown-flag arm below would swallow it. Everything after the first
		// `--` goes to the tool verbatim, including any further `--`.
		if arg == "--" {
			cfg.ToolArgs = append(cfg.ToolArgs, args[i+1:]...)
			break
		}

		next := ""
		if i+1 < len(args) {
			next = args[i+1]
		}

		switch {
		case arg == "-h" || arg == "--help":
			cfg.HelpRequested = true
			return cfg, nil

		case arg == "-C" || arg == "--dir":
			if err := requireValue("-C/--dir", next, ""); err != nil {
				return cfg, err
			}
			cfg.ProjectDir = next
			i++
		case strings.HasPrefix(arg, "--dir="):
			v := strings.TrimPrefix(arg, "--dir=")
			if err := requireInline("--dir", v, "a non-empty directory"); err != nil {
				return cfg, err
			}
			cfg.ProjectDir = v

		case arg == "-m" || arg == "--mount":
			if err := requireValue("-m/--mount", next, ""); err != nil {
				return cfg, err
			}
			cfg.ExtraMounts = append(cfg.ExtraMounts, next)
			i++
		case strings.HasPrefix(arg, "--mount="):
			v := strings.TrimPrefix(arg, "--mount=")
			if err := requireInline("--mount", v, "a non-empty directory"); err != nil {
				return cfg, err
			}
			cfg.ExtraMounts = append(cfg.ExtraMounts, v)

		case arg == "--name":
			if err := requireValue("--name", next, ""); err != nil {
				return cfg, err
			}
			cfg.CustomContainerName = next
			i++
		case strings.HasPrefix(arg, "--name="):
			v := strings.TrimPrefix(arg, "--name=")
			if err := requireInline("--name", v, "a non-empty name"); err != nil {
				return cfg, err
			}
			cfg.CustomContainerName = v

		case arg == "--session":
			if err := requireValue("--session", next, ""); err != nil {
				return cfg, err
			}
			cfg.ZellijSessionName = next
			cfg.ZellijSessionNameSet = true
			i++
		case strings.HasPrefix(arg, "--session="):
			v := strings.TrimPrefix(arg, "--session=")
			cfg.ZellijSessionName = v
			cfg.ZellijSessionNameSet = true
			if err := requireInline("--session", v, "a non-empty name"); err != nil {
				return cfg, err
			}

		case arg == RuntimeFlag:
			if err := requireValue(RuntimeFlag, next, "apple or docker"); err != nil {
				return cfg, err
			}
			cfg.ContainerRuntime = next
			i++
		case strings.HasPrefix(arg, RuntimeFlag+"="):
			v := strings.TrimPrefix(arg, RuntimeFlag+"=")
			if err := requireInline(RuntimeFlag, v, "apple or docker"); err != nil {
				return cfg, err
			}
			cfg.ContainerRuntime = v

		case arg == BuildContextFlag:
			if err := requireValue(BuildContextFlag, next, "a directory"); err != nil {
				return cfg, err
			}
			cfg.BuildContext = next
			i++
		case strings.HasPrefix(arg, BuildContextFlag+"="):
			v := strings.TrimPrefix(arg, BuildContextFlag+"=")
			if err := requireInline(BuildContextFlag, v, "a non-empty directory"); err != nil {
				return cfg, err
			}
			cfg.BuildContext = v

		case arg == "--readonly-extras":
			cfg.ReadonlyExtras = true

		case arg == "--share-skills":
			if err := requireValue("--share-skills", next, ""); err != nil {
				return cfg, err
			}
			cfg.ShareSkillsDir = next
			i++
		case strings.HasPrefix(arg, "--share-skills="):
			v := strings.TrimPrefix(arg, "--share-skills=")
			if err := requireInline("--share-skills", v, "a non-empty directory"); err != nil {
				return cfg, err
			}
			cfg.ShareSkillsDir = v

		case arg == "--share-host-claude":
			cfg.ShareHostClaude = true

		case arg == "--rebuild":
			cfg.RebuildMode = "tools"
		case strings.HasPrefix(arg, "--rebuild="):
			// No validation here; bash validates centrally in run_rebuild.
			cfg.RebuildMode = strings.TrimPrefix(arg, "--rebuild=")

		case arg == "-R":
			// Optional value: with no positionals left, any non-flag token here
			// is the mode.
			if next != "" && !strings.HasPrefix(next, "-") {
				cfg.RebuildMode = next
				i++
			} else {
				cfg.RebuildMode = "tools"
			}

		case arg == "-s" || arg == "--shell":
			cfg.ShellMode = true
		case arg == "-S" || arg == "--ssh":
			cfg.SSHMode = true
		case arg == "-w" || arg == "--worktree":
			cfg.WorktreeMode = true
		case arg == "-W" || arg == "--lock-worktrees":
			cfg.LockWorktrees = true
		case arg == "-y" || arg == "--yolo":
			cfg.YoloMode = true
		case arg == "-N" || arg == "--contained-node-modules":
			cfg.ContainedNodeModules = true

		case arg == "-t" || arg == "--tool":
			// Note: there is no --tool= form; it falls through to the
			// unknown-flag arm, as in bash.
			if err := requireValue("-t/--tool", next, ""); err != nil {
				return cfg, err
			}
			cfg.Tool = next
			i++

		case arg == "-e" || arg == "--env":
			// A leading dash can never be a valid assignment, so treat it as a
			// missing value rather than swallowing the next flag.
			if err := requireValue("-e/--env", next, "KEY=VALUE"); err != nil {
				return cfg, err
			}
			cfg.EnvFlagArgs = append(cfg.EnvFlagArgs, next)
			i++
		case strings.HasPrefix(arg, "--env="):
			// Deliberately no empty check: bash lets `--env=` through to the
			// assignment validator.
			cfg.EnvFlagArgs = append(cfg.EnvFlagArgs, strings.TrimPrefix(arg, "--env="))

		case arg == "--no-project-env":
			cfg.NoProjectEnv = true

		case arg == "-p":
			if err := requireValue("-p", next, ""); err != nil {
				return cfg, err
			}
			cfg.PortMaps = append(cfg.PortMaps, next)
			i++
		case arg == "-H":
			if err := requireValue("-H", next, ""); err != nil {
				return cfg, err
			}
			cfg.HostForwards = append(cfg.HostForwards, next)
			i++

		case arg == "--dns":
			if err := requireValue("--dns", next, ""); err != nil {
				return cfg, err
			}
			cfg.DNSServers = append(cfg.DNSServers, next)
			i++
		case strings.HasPrefix(arg, "--dns="):
			cfg.DNSServers = append(cfg.DNSServers, strings.TrimPrefix(arg, "--dns="))

		case arg == "--allow-host":
			if err := requireValue("--allow-host", next, ""); err != nil {
				return cfg, err
			}
			cfg.SrtAllowHosts = append(cfg.SrtAllowHosts, next)
			i++
		case strings.HasPrefix(arg, "--allow-host="):
			cfg.SrtAllowHosts = append(cfg.SrtAllowHosts, strings.TrimPrefix(arg, "--allow-host="))

		case arg == "--no-sandbox":
			cfg.SrtDisable = true
		case arg == "--zellij":
			cfg.ZellijMode = true
		case arg == "--new-session":
			// Force flag only: --session NAME carries the name.
			cfg.ZellijNewSession = true
		case strings.HasPrefix(arg, "--new-session="):
			_, _ = fmt.Fprintln(stderr, "error: --new-session no longer takes a name; use --session=NAME")
			return cfg, exitWith(ExitUsage)

		case arg == "-a" || arg == "--attach":
			cfg.AttachMode = true
			// Optional value. Nothing is positional, so any non-flag token here
			// is the container name.
			if next != "" && !strings.HasPrefix(next, "-") {
				cfg.AttachName = next
				i++
			}

		case strings.HasPrefix(arg, "-"):
			_, _ = fmt.Fprintf(stderr, "error: unknown flag: %s\n", arg)
			_, _ = fmt.Fprintf(stderr, "       run '%s --help' for the supported flags\n", progName)
			return cfg, exitWith(ExitUsage)

		default:
			_, _ = fmt.Fprintf(stderr, "error: positional arguments are no longer accepted: %s\n", arg)
			_, _ = fmt.Fprintf(stderr, "       use -C/--dir for the project directory:  %s -C %s\n", progName, arg)
			_, _ = fmt.Fprintf(stderr, "       use -m/--mount for extra directories:    %s -m %s\n", progName, arg)
			_, _ = fmt.Fprintf(stderr, "       (bare '%s' uses the current directory)\n", progName)
			return cfg, exitWith(ExitUsage)
		}
	}

	if err := validate(&cfg, stderr); err != nil {
		return cfg, err
	}
	return cfg, nil
}

var zellijSessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ValidateZellijSessionName mirrors validate_zellij_session_name
// (claude-contained:375-388).
func ValidateZellijSessionName(name string, stderr io.Writer) error {
	if name == "" {
		_, _ = fmt.Fprintln(stderr, "error: Zellij session name cannot be empty")
		return exitWith(ExitUsage)
	}
	if name[0] == '-' || !zellijSessionNamePattern.MatchString(name) {
		_, _ = fmt.Fprintf(stderr, "error: invalid Zellij session name: %s\n", name)
		_, _ = fmt.Fprintln(stderr, "       Use only letters, numbers, '_', '.', and '-'; do not start with '-'.")
		return exitWith(ExitUsage)
	}
	return nil
}

// validate mirrors claude-contained:826-878. Order matters: each check's
// message and exit status is observable, and the --name rewrite happens here
// rather than at use.
func validate(cfg *Config, stderr io.Writer) error {
	if cfg.ZellijNewSession && !cfg.ZellijMode {
		_, _ = fmt.Fprintln(stderr, "error: --new-session is valid only with --zellij")
		return exitWith(ExitUsage)
	}
	if cfg.ZellijSessionNameSet && !cfg.ZellijMode {
		_, _ = fmt.Fprintln(stderr, "error: --session is valid only with --zellij")
		return exitWith(ExitUsage)
	}
	if cfg.ZellijMode && cfg.AttachMode && cfg.ShellMode {
		_, _ = fmt.Fprintln(stderr, "error: --zellij --attach cannot be combined with --shell")
		return exitWith(ExitUsage)
	}
	// Under --zellij the target is a session, named only by --session.
	// Accepting a name on -a as well would give one token two meanings again.
	if cfg.ZellijMode && cfg.AttachName != "" {
		_, _ = fmt.Fprintln(stderr, "error: -a/--attach takes no name with --zellij; use --session=NAME")
		return exitWith(ExitUsage)
	}
	if cfg.AttachMode && cfg.CustomContainerName != "" {
		_, _ = fmt.Fprintln(stderr, "error: --name cannot be combined with -a/--attach")
		_, _ = fmt.Fprintln(stderr, "       --name names a new container; --attach reconnects to an existing one.")
		return exitWith(ExitUsage)
	}
	if cfg.ZellijSessionNameSet {
		if err := ValidateZellijSessionName(cfg.ZellijSessionName, stderr); err != nil {
			return err
		}
	}
	// --name takes a bare name; the container name downstream expects the aic-
	// prefix and the same sanitizing every generated name gets.
	if cfg.CustomContainerName != "" {
		cfg.CustomContainerName = "aic-" + host.SanitizeFolderName(strings.TrimPrefix(cfg.CustomContainerName, "aic-"))
	}
	if cfg.ZellijMode && cfg.AttachMode && len(cfg.EnvFlagArgs) > 0 {
		_, _ = fmt.Fprintln(stderr, "error: --env cannot be combined with --zellij --attach")
		_, _ = fmt.Fprintln(stderr, "       Attaching starts a Zellij client; the pane keeps the environment it was")
		_, _ = fmt.Fprintln(stderr, "       created with, so the variable would silently never reach the tool.")
		_, _ = fmt.Fprintln(stderr, "       Set it when the session is created instead.")
		return exitWith(ExitUsage)
	}
	return nil
}
