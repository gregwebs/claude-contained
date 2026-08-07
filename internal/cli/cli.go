// Package cli parses the launcher's flag-only command line into a Config.
//
// The whole flag surface is parsed here even though ticket 02 only executes the
// basic run path. Splitting the parser would mean porting it twice, and the
// flag-only CLI is one unit: `--` handling, the require_value rule and the
// unknown-flag arm all interact.
package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"claude-contained/internal/diagnostic"
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

// LayerFlag names the project's tooling layer directory. BuildContextFlag is
// the precedent for a launcher-only value flag; "tooling layer" is CONTEXT.md's
// term and --layer its natural short form.
const LayerFlag = "--layer"

// BuildLayerFlag skips the build confirmation and builds; NoLayerFlag ignores
// the layer and runs the base image. Deliberately imperative and deliberately
// matching the existing --no-project-env / --no-sandbox convention.
//
// Neither has an environment variable, and that is a decision rather than an
// omission. An environment variable is a stored approval by another name:
// exported once in a shell profile and forgotten, it defeats the confirmation
// the spec's Trust section exists to establish. NoLayerFlag is refused one for
// the mirror-image reason -- silently skipping the layer produces a container
// that looks healthy while missing its toolchain.
//
// Rejected names: --approve-layer (approval implies storage, which the spec
// refuses) and --no-confirm-layer (a double negative).
const (
	BuildLayerFlag = "--build-layer"
	NoLayerFlag    = "--no-layer"
)

const (
	// LogLevelFlag selects contributor-facing diagnostic detail.
	LogLevelFlag = "--log-level"
	// LogFileFlag moves the diagnostic stream to a secured file.
	LogFileFlag = "--log-file"
)

type syntaxFailureKind uint8

const (
	syntaxRequiredValue syntaxFailureKind = iota
	syntaxNewSessionValue
	syntaxUnknownFlag
	syntaxToolRemoved
	syntaxYoloRemoved
	syntaxCommandFlag
)

// syntaxFailure keeps only the source facts needed to render a diagnosis.
// Rendering belongs to Validate so Parse cannot write before the diagnostic
// stream is configured.
type syntaxFailure struct {
	kind  syntaxFailureKind
	flag  string
	what  string
	value string
}

type parseState struct {
	progName string
	failures []syntaxFailure
	// commandFromSeparator is true when the container command was introduced by
	// `--` rather than by the bare-positional rule. -a's own token can only ever
	// be a container name, so a bare-positional command after it is ambiguous
	// with a second name; a `--`-introduced one is not. See ValidateContext's
	// AttachMode case and docs/adr/0009.
	commandFromSeparator bool
}

// Config is the parsed command line.
type Config struct {
	ShellMode            bool
	SSHMode              bool
	WorktreeMode         bool
	LockWorktrees        bool
	ContainedNodeModules bool
	AttachMode           bool
	AttachName           string
	CustomContainerName  string
	ProjectDir           string
	ExtraMounts          []string
	// Command is the container command: the first token not consumed by a flag
	// terminates flag parsing, and everything from it onward (verbatim, including
	// any `-flags` and any further `--`) lands here. Empty means no command was
	// given, so the image's own CMD runs. See docs/adr/0009.
	Command []string
	// ContainerRuntime is --container-runtime's value, unvalidated: the accepted
	// names live in internal/runtime, which diagnoses a bad one after --help has
	// had its chance.
	ContainerRuntime string
	// BuildContext is --build-context's value, unvalidated: internal/host checks
	// for a Dockerfile when a rebuild actually needs one.
	BuildContext string
	// LayerDir is --layer's value, unvalidated: internal/host checks for a
	// Dockerfile when a run actually needs one, the same split as BuildContext.
	LayerDir string
	// BuildLayer skips the build confirmation; NoLayer ignores the layer
	// entirely. Inert with --attach and --rebuild, which return above the layer
	// step -- not refused, because unlike --name with --attach they are merely
	// unused rather than actively misleading.
	BuildLayer           bool
	NoLayer              bool
	LogLevel             string
	LogLevelSet          bool
	LogFile              string
	LogOnly              bool
	ZellijMode           bool
	ZellijNewSession     bool
	ZellijSessionName    string
	ZellijSessionNameSet bool
	RebuildMode          string
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
	parse         parseState
}

// Parse is the silent syntactic recording pass. It consumes launcher tokens,
// records ordered failures, and continues far enough to derive the final
// runtime selection. Validate is the sole owner of parser and semantic
// reporting.
//
// progName is the single installed launcher name used later in deferred fix-it
// hints. shareHostClaudeEnv carries CLAUDE_CONTAINED_SHARE_HOST_CLAUDE.
func Parse(args []string, progName string, shareHostClaudeEnv bool) Config {
	cfg := Config{
		RebuildMode:     "none",
		ShareHostClaude: shareHostClaudeEnv,
		parse:           parseState{progName: progName},
	}

	// Callers for ordinary separate required-value flags advance past any
	// present token, including a dash-leading attempted value. RuntimeFlag has
	// its own arm below because a dash-leading attempted runtime value is the
	// one exception to that consumption rule.
	recordRequiredValue := func(flag, value, what string) bool {
		if value == "" || strings.HasPrefix(value, "-") {
			if what == "" {
				what = "a value"
			}
			cfg.parse.failures = append(cfg.parse.failures, syntaxFailure{
				kind: syntaxRequiredValue,
				flag: flag,
				what: what,
			})
			return false
		}
		return true
	}

	recordInlineValue := func(flag, value, what string) {
		if value == "" {
			cfg.parse.failures = append(cfg.parse.failures, syntaxFailure{
				kind: syntaxRequiredValue,
				flag: flag,
				what: what,
			})
		}
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// `--` matches -* and must be handled before any flag dispatch, or the
		// unknown-flag arm below would swallow it. Everything after the first
		// `--` is the container command verbatim, including any further `--`.
		if arg == "--" {
			cfg.Command = append(cfg.Command, args[i+1:]...)
			cfg.parse.commandFromSeparator = true
			// A dash-leading first command token is the only shape the old `--`
			// passthrough could produce (`-- --model sonnet`); catch it precisely.
			if len(cfg.Command) > 0 && strings.HasPrefix(cfg.Command[0], "-") {
				cfg.parse.failures = append(cfg.parse.failures, syntaxFailure{
					kind:  syntaxCommandFlag,
					flag:  cfg.Command[0],
					value: strings.Join(cfg.Command, " "),
				})
			}
			break
		}

		// The first token not consumed by a flag terminates flag parsing;
		// everything from it on is the container command, verbatim. Non-dash
		// because flag values are consumed via i++ and never reach the top of
		// the loop, and dash tokens are either known flags or (after `--`)
		// already handled above.
		if !strings.HasPrefix(arg, "-") {
			cfg.Command = args[i:]
			break
		}

		hasNext := i+1 < len(args)
		next := ""
		if hasNext {
			next = args[i+1]
		}

		switch {
		case arg == "-h" || arg == "--help":
			// Help is effective only when reached before the first syntax failure.
			// Parsing continues because later tokens can still select the runtime
			// profile whose help text the front end prints.
			if len(cfg.parse.failures) == 0 {
				cfg.HelpRequested = true
			}

		case arg == "-C" || arg == "--dir":
			if recordRequiredValue("-C/--dir", next, "") {
				cfg.ProjectDir = next
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, "--dir="):
			v := strings.TrimPrefix(arg, "--dir=")
			cfg.ProjectDir = v
			recordInlineValue("--dir", v, "a non-empty directory")

		case arg == "-m" || arg == "--mount":
			if recordRequiredValue("-m/--mount", next, "") {
				cfg.ExtraMounts = append(cfg.ExtraMounts, next)
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, "--mount="):
			v := strings.TrimPrefix(arg, "--mount=")
			cfg.ExtraMounts = append(cfg.ExtraMounts, v)
			recordInlineValue("--mount", v, "a non-empty directory")

		case arg == "--name":
			if recordRequiredValue("--name", next, "") {
				cfg.CustomContainerName = next
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, "--name="):
			v := strings.TrimPrefix(arg, "--name=")
			cfg.CustomContainerName = v
			recordInlineValue("--name", v, "a non-empty name")

		case arg == "--session":
			if recordRequiredValue("--session", next, "") {
				cfg.ZellijSessionName = next
				cfg.ZellijSessionNameSet = true
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, "--session="):
			v := strings.TrimPrefix(arg, "--session=")
			cfg.ZellijSessionName = v
			cfg.ZellijSessionNameSet = true
			recordInlineValue("--session", v, "a non-empty name")

		case arg == RuntimeFlag:
			// Unlike every other required-value flag, a malformed separate
			// runtime occurrence leaves a dash-leading token for normal parsing.
			// This preserves the scanner grammar that made a following help,
			// boundary, or runtime flag visible.
			if !hasNext || strings.HasPrefix(next, "-") {
				recordRequiredValue(RuntimeFlag, next, "apple or docker")
				break
			}
			cfg.ContainerRuntime = next
			recordRequiredValue(RuntimeFlag, next, "apple or docker")
			i++
		case strings.HasPrefix(arg, RuntimeFlag+"="):
			v := strings.TrimPrefix(arg, RuntimeFlag+"=")
			cfg.ContainerRuntime = v
			recordInlineValue(RuntimeFlag, v, "apple or docker")

		case arg == BuildContextFlag:
			if recordRequiredValue(BuildContextFlag, next, "a directory") {
				cfg.BuildContext = next
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, BuildContextFlag+"="):
			v := strings.TrimPrefix(arg, BuildContextFlag+"=")
			cfg.BuildContext = v
			recordInlineValue(BuildContextFlag, v, "a non-empty directory")

		// Mirrors BuildContextFlag exactly, including its asymmetric `what`
		// strings: the separate form asks for "a directory", the inline form for
		// "a non-empty directory", because `--layer=` is a different mistake
		// from `--layer` with nothing after it.
		case arg == LayerFlag:
			if recordRequiredValue(LayerFlag, next, "a directory") {
				cfg.LayerDir = next
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, LayerFlag+"="):
			v := strings.TrimPrefix(arg, LayerFlag+"=")
			cfg.LayerDir = v
			recordInlineValue(LayerFlag, v, "a non-empty directory")

		case arg == BuildLayerFlag:
			cfg.BuildLayer = true
		case arg == NoLayerFlag:
			cfg.NoLayer = true

		case arg == LogLevelFlag:
			if recordRequiredValue(LogLevelFlag, next, "debug, info, warn, error, or off") {
				cfg.LogLevel = next
				cfg.LogLevelSet = true
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, LogLevelFlag+"="):
			v := strings.TrimPrefix(arg, LogLevelFlag+"=")
			if v == "" {
				recordInlineValue(LogLevelFlag, v, "debug, info, warn, error, or off")
			} else {
				cfg.LogLevel = v
				cfg.LogLevelSet = true
			}

		case arg == LogFileFlag:
			if recordRequiredValue(LogFileFlag, next, "a path") {
				cfg.LogFile = next
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, LogFileFlag+"="):
			v := strings.TrimPrefix(arg, LogFileFlag+"=")
			if v == "" {
				recordInlineValue(LogFileFlag, v, "a non-empty path")
			} else {
				cfg.LogFile = v
			}

		case arg == "--log-only":
			cfg.LogOnly = true

		case arg == "--readonly-extras":
			cfg.ReadonlyExtras = true

		case arg == "--share-skills":
			if recordRequiredValue("--share-skills", next, "") {
				cfg.ShareSkillsDir = next
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, "--share-skills="):
			v := strings.TrimPrefix(arg, "--share-skills=")
			cfg.ShareSkillsDir = v
			recordInlineValue("--share-skills", v, "a non-empty directory")

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
			cfg.parse.failures = append(cfg.parse.failures, syntaxFailure{kind: syntaxYoloRemoved})
		case arg == "-N" || arg == "--contained-node-modules":
			cfg.ContainedNodeModules = true

		case arg == "-t" || arg == "--tool":
			// Note: there is no --tool= form; it falls through to the
			// unknown-flag arm, as in bash. Consume the value so it does not
			// also start a positional command; record the removal regardless.
			cfg.parse.failures = append(cfg.parse.failures, syntaxFailure{kind: syntaxToolRemoved})
			if hasNext {
				i++
			}

		case arg == "-e" || arg == "--env":
			if recordRequiredValue("-e/--env", next, "KEY=VALUE") {
				cfg.EnvFlagArgs = append(cfg.EnvFlagArgs, next)
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, "--env="):
			// Deliberately no empty check: bash lets `--env=` through to the
			// assignment validator.
			cfg.EnvFlagArgs = append(cfg.EnvFlagArgs, strings.TrimPrefix(arg, "--env="))

		case arg == "--no-project-env":
			cfg.NoProjectEnv = true

		case arg == "-p":
			if recordRequiredValue("-p", next, "") {
				cfg.PortMaps = append(cfg.PortMaps, next)
			}
			if hasNext {
				i++
			}
		case arg == "-H":
			if recordRequiredValue("-H", next, "") {
				cfg.HostForwards = append(cfg.HostForwards, next)
			}
			if hasNext {
				i++
			}

		case arg == "--dns":
			if recordRequiredValue("--dns", next, "") {
				cfg.DNSServers = append(cfg.DNSServers, next)
			}
			if hasNext {
				i++
			}
		case strings.HasPrefix(arg, "--dns="):
			cfg.DNSServers = append(cfg.DNSServers, strings.TrimPrefix(arg, "--dns="))

		case arg == "--allow-host":
			if recordRequiredValue("--allow-host", next, "") {
				cfg.SrtAllowHosts = append(cfg.SrtAllowHosts, next)
			}
			if hasNext {
				i++
			}
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
			cfg.parse.failures = append(cfg.parse.failures, syntaxFailure{kind: syntaxNewSessionValue})

		case arg == "-a" || arg == "--attach":
			cfg.AttachMode = true
			// Optional value. Nothing is positional, so any non-flag token here
			// is the container name.
			if next != "" && !strings.HasPrefix(next, "-") {
				cfg.AttachName = next
				i++
			}

		case strings.HasPrefix(arg, "-"):
			cfg.parse.failures = append(cfg.parse.failures, syntaxFailure{
				kind:  syntaxUnknownFlag,
				value: arg,
			})
		}
	}
	return cfg
}

// CommandSource classifies where the container command came from, for
// diagnostics. It never carries the command itself -- see docs/adr/0009 and
// the diagnostic-stream rules in AGENTS.md.
func CommandSource(cfg Config) string {
	switch {
	case cfg.ShellMode:
		return "shell"
	case len(cfg.Command) > 0:
		return "explicit"
	default:
		return "image-default"
	}
}

var zellijSessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ValidateZellijSessionName mirrors validate_zellij_session_name
// (claude-contained:375-388).
func ValidateZellijSessionName(name string, stderr io.Writer) error {
	return ValidateZellijSessionNameContext(context.Background(), name, stderr)
}

// ValidateZellijSessionNameContext is the diagnostic-aware validation seam.
// The compatibility wrapper above keeps callers that do not own a configured
// context silent through diagnostic's discard fallback.
func ValidateZellijSessionNameContext(ctx context.Context, name string, stderr io.Writer) error {
	if name == "" {
		_, _ = fmt.Fprintln(stderr, "error: Zellij session name cannot be empty")
		diagnostic.For(ctx, diagnostic.ComponentCLI).Warn("command line validation failed",
			diagnostic.String("validation_kind", "zellij-session-empty"))
		return exitWith(ExitUsage)
	}
	if name[0] == '-' || !zellijSessionNamePattern.MatchString(name) {
		_, _ = fmt.Fprintf(stderr, "error: invalid Zellij session name: %s\n", name)
		_, _ = fmt.Fprintln(stderr, "       Use only letters, numbers, '_', '.', and '-'; do not start with '-'.")
		diagnostic.For(ctx, diagnostic.ComponentCLI).Warn("command line validation failed",
			diagnostic.String("validation_kind", "zellij-session-invalid"))
		return exitWith(ExitUsage)
	}
	return nil
}

// Validate is the sole reporting pass for deferred parser failures and CLI
// semantics. Order matters: each check's message and exit status is observable,
// and the --name rewrite happens here rather than at use.
func Validate(cfg *Config, stderr io.Writer) error {
	return ValidateContext(context.Background(), cfg, stderr)
}

// ValidateContext reports the same byte-identical user diagnostics as
// Validate and adds only closed validation classifications to the configured
// diagnostic stream.
func ValidateContext(ctx context.Context, cfg *Config, stderr io.Writer) error {
	fail := func(kind string) error {
		diagnostic.For(ctx, diagnostic.ComponentCLI).Warn("command line validation failed",
			diagnostic.String("validation_kind", kind))
		return exitWith(ExitUsage)
	}

	// Effective help short-circuits both deferred syntax failures encountered
	// later in argv and semantic validation. The front end prints the selected
	// runtime profile's help before calling Validate; this guard keeps the seam's
	// behavior complete when it is used directly.
	if cfg.HelpRequested {
		return nil
	}
	if len(cfg.parse.failures) > 0 {
		failure := cfg.parse.failures[0]
		kind := "invalid-syntax"
		switch failure.kind {
		case syntaxRequiredValue:
			kind = "required-value"
			_, _ = fmt.Fprintf(stderr, "error: %s requires %s\n", failure.flag, failure.what)
		case syntaxNewSessionValue:
			kind = "new-session-value"
			_, _ = fmt.Fprintln(stderr, "error: --new-session no longer takes a name; use --session=NAME")
		case syntaxUnknownFlag:
			kind = "unknown-flag"
			_, _ = fmt.Fprintf(stderr, "error: unknown flag: %s\n", failure.value)
			_, _ = fmt.Fprintf(stderr, "       run '%s --help' for the supported flags\n", cfg.parse.progName)
		case syntaxToolRemoved:
			kind = "tool-flag-removed"
			_, _ = fmt.Fprintln(stderr, "error: -t/--tool is no longer accepted")
			_, _ = fmt.Fprintf(stderr, "       name the program positionally:  %s <program>\n", cfg.parse.progName)
		case syntaxYoloRemoved:
			kind = "yolo-flag-removed"
			_, _ = fmt.Fprintln(stderr, "error: -y/--yolo is no longer accepted")
			_, _ = fmt.Fprintf(stderr, "       pass the flag to the program:  %s claude --dangerously-skip-permissions\n", cfg.parse.progName)
		case syntaxCommandFlag:
			kind = "command-starts-with-flag"
			_, _ = fmt.Fprintf(stderr, "error: command cannot start with a flag: %s\n", failure.flag)
			_, _ = fmt.Fprintf(stderr, "       name the program first:  %s claude %s\n", cfg.parse.progName, failure.value)
		}
		return fail(kind)
	}

	if cfg.ZellijNewSession && !cfg.ZellijMode {
		_, _ = fmt.Fprintln(stderr, "error: --new-session is valid only with --zellij")
		return fail("new-session-without-zellij")
	}
	if cfg.ZellijSessionNameSet && !cfg.ZellijMode {
		_, _ = fmt.Fprintln(stderr, "error: --session is valid only with --zellij")
		return fail("session-without-zellij")
	}
	if cfg.ZellijMode && cfg.AttachMode && cfg.ShellMode {
		_, _ = fmt.Fprintln(stderr, "error: --zellij --attach cannot be combined with --shell")
		return fail("zellij-attach-shell")
	}
	// Under --zellij the target is a session, named only by --session.
	// Accepting a name on -a as well would give one token two meanings again.
	if cfg.ZellijMode && cfg.AttachName != "" {
		_, _ = fmt.Fprintln(stderr, "error: -a/--attach takes no name with --zellij; use --session=NAME")
		return fail("zellij-attach-name")
	}
	if cfg.AttachMode && cfg.CustomContainerName != "" {
		_, _ = fmt.Fprintln(stderr, "error: --name cannot be combined with -a/--attach")
		_, _ = fmt.Fprintln(stderr, "       --name names a new container; --attach reconnects to an existing one.")
		return fail("attach-custom-name")
	}
	if cfg.ZellijSessionNameSet {
		if err := ValidateZellijSessionNameContext(ctx, cfg.ZellijSessionName, stderr); err != nil {
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
		return fail("zellij-attach-environment")
	}
	// A command has nowhere to go alongside any of these: -s/-R substitute their
	// own command, --zellij --attach and -a reconnect to something already
	// running. Each is a usage error rather than a silent discard.
	if len(cfg.Command) > 0 {
		switch {
		case cfg.ShellMode:
			_, _ = fmt.Fprintln(stderr, "error: -s/--shell cannot be combined with a command")
			_, _ = fmt.Fprintln(stderr, "       -s runs a debug shell in place of the container command.")
			return fail("shell-with-command")
		case cfg.RebuildMode != "none":
			_, _ = fmt.Fprintln(stderr, "error: -R/--rebuild cannot be combined with a command")
			_, _ = fmt.Fprintln(stderr, "       --rebuild builds an image and exits; it runs no command.")
			_, _ = fmt.Fprintf(stderr, "       select the mode with --rebuild=MODE:  %s --rebuild=full\n", cfg.parse.progName)
			return fail("rebuild-with-command")
		case cfg.ZellijMode && cfg.AttachMode:
			_, _ = fmt.Fprintln(stderr, "error: --zellij --attach cannot be combined with a command")
			_, _ = fmt.Fprintln(stderr, "       Attaching reconnects to an existing session; the command would never run.")
			return fail("zellij-attach-with-command")
		case cfg.AttachMode && !cfg.parse.commandFromSeparator:
			// A second bare token after -a NAME. Commands ARE allowed with -a, but
			// only when introduced by -- (docs/adr/0009's "-a/-R require --"): a
			// bare token can only be the container name, and a second one has no
			// meaning.
			_, _ = fmt.Fprintln(stderr, "error: -a/--attach takes only a container name before a command")
			_, _ = fmt.Fprintf(stderr, "       introduce the command with --:  %s -a NAME -- npm test\n", cfg.parse.progName)
			return fail("attach-bare-command")
		}
	}
	// Last, immediately before the return, and that placement is load-bearing
	// rather than stylistic. This function's message order is observable, and
	// "after the Zellij checks" would have been ambiguous -- there are Zellij
	// checks on both sides of the --name rewrite above. Last is the only
	// position that is strictly additive: every input that failed before still
	// fails on the same earlier check with the same message and the same
	// validation_kind, so no existing test or golden can move. The --name
	// rewrite running first is harmless, since a conflict exits.
	if cfg.NoLayer && (cfg.LayerDir != "" || cfg.BuildLayer) {
		_, _ = fmt.Fprintf(stderr, "error: %s cannot be combined with %s or %s\n",
			NoLayerFlag, LayerFlag, BuildLayerFlag)
		_, _ = fmt.Fprintf(stderr, "       %s runs the base image; the others select or build a tooling layer.\n",
			NoLayerFlag)
		return fail("layer-flag-conflict")
	}
	return nil
}
