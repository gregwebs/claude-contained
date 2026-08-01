package main

// goldenfixture_test.go builds the per-case fixture, the on-PATH
// runtime stubs, the clearEnv helper, the filesystem manifest
// walk and the textual normalizer that golden_test.go's driver
// composes into one pipeline per case. See golden_test.go for the pipeline
// itself and goldencase_test.go for the 59-case table.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"claude-contained/internal/host"
)

// launcherEnvVars is every environment variable host.Probe or the Docker SSH
// path read (verified by grepping os.Getenv/os.LookupEnv across production
// code, mirroring harness.sh's run_side blacklist and its README footgun
// note). A new launcher-read environment variable must be added here, or the
// case that first touches it silently bakes the developer's own value into a
// golden.
var launcherEnvVars = []string{
	"CLAUDE_MEMORY",
	"CLAUDE_DNS",
	"AI_GH_TOKEN",
	"CLAUDE_CONTAINED_SHARE_HOST_CLAUDE",
	"CLAUDE_CONTAINED_RUNTIME",
	"CLAUDE_CONTAINED_LOG_LEVEL",
	"CLAUDE_CONTAINED_BUILD_CONTEXT",
	"SSH_AUTH_SOCK",
}

// clearEnv unsets each key for the duration of the test, restoring whatever
// was there before in t.Cleanup.
//
// t.Setenv(k, "") is not "cleared": internal/host/state.go:53 reads CLAUDE_DNS
// with os.LookupEnv and keeps a dnsSet bool, so "set to empty" (suppress the
// runtime default resolver) and "not set" (use it) are different behaviors.
// Corpus cases 16 and 17 exist to tell those apart, so this helper calls
// os.Unsetenv rather than t.Setenv(k, "").
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		prev, had := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, prev)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

// --- fixture roots and stub runtimes ---------------------------------------

// goldenFixture is one case's isolated filesystem: a fresh HOME, project
// directory and stub PATH, all under one t.TempDir() root with fixed
// basenames. The basenames are load-bearing, not cosmetic: the container name
// embeds host.SanitizeFolderName(basename(project dir)), so a random
// mktemp-style basename would put the temp directory's own name into every
// golden's --name argument (isolate.sh:146-157 gives the same reason).
type goldenFixture struct {
	root, home, proj, stub string
	// stubList and stubInspectDir are fixed, root-relative locations the
	// golden stub scripts read via GOLDEN_LIST_OUTPUT/GOLDEN_INSPECT_DIR.
	// Fixed paths (not under home, which a case's own Setup may rewrite)
	// keep the stub configuration stable regardless of what a case does to
	// HOME.
	stubList, stubInspectDir string
}

func newGoldenFixture(t *testing.T) goldenFixture {
	t.Helper()
	// host.ResolvePath: macOS's /var -> /private/var symlink, matching
	// isolate.sh:167-170 and run_test.go's existing fixtures. Mount sources
	// and the manifest's own labels would never agree otherwise.
	root := host.ResolvePath(t.TempDir())
	fx := goldenFixture{
		root:           root,
		home:           filepath.Join(root, "home"),
		proj:           filepath.Join(root, "project"),
		stub:           filepath.Join(root, "stub"),
		stubList:       filepath.Join(root, "golden-stub-list"),
		stubInspectDir: filepath.Join(root, "golden-stub-inspect"),
	}
	for _, d := range []string{fx.home, fx.proj, fx.stub, fx.stubInspectDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeGoldenStubs(t, fx.stub)
	return fx
}

// writeGoldenStubs installs fake `container` and `docker` executables that
// answer only the three subcommands the launcher still shells out for
// directly: EnsureUp's liveness probe, List and InspectEnv. The `run`/`exec`/
// `build` invocations never reach these scripts at all -- the injected
// runner (captureRunner in golden_test.go) and the swapped replaceProcess
// carry those instead, which is what makes DIFF_ARGV_LOG's whole env-var
// contract (lib/isolate.sh) unnecessary here. Port of
// lib/isolate.sh:write_stub_runtimes, minus the branches the runner seam
// replaces.
func writeGoldenStubs(t *testing.T, dir string) {
	t.Helper()
	container := "#!/bin/sh\n" +
		"set -u\n" +
		"sub=\"${1:-}\"\n" +
		"case \"$sub\" in\n" +
		"  system) exit 0 ;;\n" +
		"  list)\n" +
		"    [ -n \"${GOLDEN_LIST_OUTPUT:-}\" ] && [ -f \"$GOLDEN_LIST_OUTPUT\" ] && cat \"$GOLDEN_LIST_OUTPUT\"\n" +
		"    exit 0 ;;\n" +
		"  inspect)\n" +
		"    name=\"\"\n" +
		"    for a in \"$@\"; do name=\"$a\"; done\n" +
		"    envfile=\"${GOLDEN_INSPECT_DIR:-}/${name}.env\"\n" +
		"    printf '[{\"configuration\":{\"initProcess\":{\"environment\":['\n" +
		"    if [ -f \"$envfile\" ]; then\n" +
		"      first=1\n" +
		"      while IFS= read -r line; do\n" +
		"        [ -z \"$line\" ] && continue\n" +
		"        [ \"$first\" -eq 0 ] && printf ','\n" +
		"        printf '\"%s\"' \"$line\"\n" +
		"        first=0\n" +
		"      done < \"$envfile\"\n" +
		"    fi\n" +
		"    printf ']}}}]\\n'\n" +
		"    exit 0 ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"

	docker := "#!/bin/sh\n" +
		"set -u\n" +
		"sub=\"${1:-}\"\n" +
		"case \"$sub\" in\n" +
		"  info) exit 0 ;;\n" +
		"  ps)\n" +
		"    [ -n \"${GOLDEN_LIST_OUTPUT:-}\" ] && [ -f \"$GOLDEN_LIST_OUTPUT\" ] && cat \"$GOLDEN_LIST_OUTPUT\"\n" +
		"    exit 0 ;;\n" +
		"  inspect)\n" +
		"    name=\"\"\n" +
		"    for a in \"$@\"; do name=\"$a\"; done\n" +
		"    envfile=\"${GOLDEN_INSPECT_DIR:-}/${name}.env\"\n" +
		"    [ -f \"$envfile\" ] && cat \"$envfile\"\n" +
		"    exit 0 ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"

	if err := os.WriteFile(filepath.Join(dir, "container"), []byte(container), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0o755); err != nil {
		t.Fatal(err)
	}
}

// --- fixture-writing helpers shared across case Setup functions -----------

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// --- the filesystem manifest ------------------------------------------------

// gitRelativePath reports whether path lies at or under a ".git" directory
// component beneath root, and if so, path's location relative to that .git
// directory (slash-separated). ok=false for anything outside a .git tree and
// for a bare ".git" directory itself (gitRel == "" in that case, which
// retainedUnderGit treats as never-retained).
func gitRelativePath(root, path string) (gitRel string, ok bool) {
	rel := strings.TrimPrefix(path, root+string(os.PathSeparator))
	if rel == path {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, p := range parts {
		if p == ".git" {
			return strings.Join(parts[i+1:], "/"), true
		}
	}
	return "", false
}

// retainedUnderGit is the narrowed manifest scope: everything
// under a .git directory is pruned from the walk except
// .git/worktrees/*/locked, .git/claude-contained-worktree-locks.lock/** and
// any *.mid-run-snapshot anywhere. This is deliberately narrower than
// lib/manifest.sh, which also listed (structurally) every other .git entry:
// a committed golden is compared against a different git version on CI,
// where the set of .git/hooks/*.sample files and other admin content differs
// by version, unrelated to anything the launcher does.
//
// Everything the launcher itself writes under a .git directory lands in
// exactly these two locations: addAutoLockOwner/removeAutoLockOwner operate
// on <gitdir>/locked (internal/host/worktreelock.go:120-127) and
// acquireMutex on <repo>/.git/claude-contained-worktree-locks.lock (:33, :37,
// :57). Every other `git` invocation in production is read-only
// (internal/host/state.go:152, plan/placeholder.go, worktree.go:50,
// worktreelock.go:121) -- a future change that starts writing elsewhere
// under .git must revisit this list.
func retainedUnderGit(gitRel string) bool {
	if gitRel == "" {
		return false
	}
	if strings.HasSuffix(gitRel, ".mid-run-snapshot") {
		return true
	}
	parts := strings.Split(gitRel, "/")
	if len(parts) == 3 && parts[0] == "worktrees" && parts[2] == "locked" {
		return true
	}
	if parts[0] == "claude-contained-worktree-locks.lock" {
		return true
	}
	return false
}

// shouldInlineContent mirrors manifest.sh:66-73: content is inlined for
// every retained file except under .git, where only "locked" and
// "*.mid-run-snapshot" ever get their bytes shown (the only two basenames
// retainedUnderGit ever admits in the first place, except for other files
// living alongside the mutex owner file, which get a type/mode line only).
func shouldInlineContent(root, path string) bool {
	_, underGit := gitRelativePath(root, path)
	if !underGit {
		return true
	}
	base := filepath.Base(path)
	return base == "locked" || strings.HasSuffix(base, ".mid-run-snapshot")
}

func modeString(info os.FileInfo) string {
	return fmt.Sprintf("%03o", info.Mode().Perm())
}

func inlineContentLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	text := strings.TrimSuffix(string(data), "\n")
	return strings.Split(text, "\n")
}

func rewriteSymlinkTarget(root, label, target string) string {
	sep := string(os.PathSeparator)
	switch {
	case target == root:
		return label
	case strings.HasPrefix(target, root+sep):
		return label + "/" + filepath.ToSlash(strings.TrimPrefix(target, root+sep))
	default:
		return target
	}
}

// goldenManifest is a direct port of lib/manifest.sh's capture_manifest, with
// the .git narrowing above. Two passes, deliberately not one: bash's
// `find root ... | sort` sorts full path *strings* flatly, which disagrees
// with filepath.Walk's per-directory (hierarchical) order whenever a
// filename containing '.' sorts differently against a sibling directory than
// their full paths would flatly (e.g. "a.txt" vs "a/b"). Collecting the kept
// absolute paths first and then sort.Strings-ing them reproduces the flat
// order exactly.
func goldenManifest(root, label string) []string {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil
	}

	var paths []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // vanished between readdir and stat; find would not report it either.
		}
		if gitRel, underGit := gitRelativePath(root, path); underGit && !retainedUnderGit(gitRel) {
			return nil // do not emit a line; still descend (no SkipDir) so a
			// retained descendant further down is not missed.
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)

	lines := make([]string, 0, len(paths))
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		rel := label
		if path != root {
			relPath, _ := filepath.Rel(root, path)
			rel = label + "/" + filepath.ToSlash(relPath)
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				target = ""
			}
			// A symlink's own mode bits are not portable and not meaningful:
			// no production code path ever sets or depends on one (a
			// symlink's target is what governs access, never the link
			// itself), and the two platforms don't even agree on what value
			// to report. Confirmed empirically running this suite under
			// linux/arm64 (Apple's `container` CLI, golang:1.24): the kernel
			// always reports a freshly created symlink as 777 regardless of
			// umask, while macOS/BSD reports the umask-masked value (755
			// under the standard 022). Rendering the real, OS-reported value
			// here would make every symlink line disagree between the
			// macOS-generated goldens and CI's Linux runner for a reason
			// that has nothing to do with the launcher -- so it is
			// normalized at the source, the same way N8-N10 exist because a
			// committed golden is compared across machines ("an
			// eleventh is an obvious addition, not a guess").
			lines = append(lines, fmt.Sprintf("%s symlink <LMODE> -> %s", rel, rewriteSymlinkTarget(root, label, target)))
		case info.IsDir():
			lines = append(lines, fmt.Sprintf("%s dir %s", rel, modeString(info)))
		case info.Mode().IsRegular():
			line := fmt.Sprintf("%s file %s", rel, modeString(info))
			if shouldInlineContent(root, path) {
				if data, err := os.ReadFile(path); err == nil {
					for _, l := range inlineContentLines(data) {
						line += "\n  | " + l
					}
				}
			}
			lines = append(lines, line)
		}
	}
	return lines
}

// --- normalization -----------------------------------------------------------

// normContext is everything normalizeText needs to neutralize one case's
// host-variance. arch and the two numeric substitutions (N8/N9) are read
// once per process; proj/home/root/phash are fixture-specific.
type normContext struct {
	proj, home, root string
	phash            string // host.PathHash8 of the case's resolved -C target, "" if none
	arch             string // host.Probe().Arch -- N10
	uid, gid         string // strconv.Itoa(os.Getuid()/os.Getgid()) -- N8/N9
}

var (
	// N5: the container name's `date +%H%M` minute stamp.
	reContainerTime = regexp.MustCompile(`(?m)(aic-[A-Za-z0-9-]*)-[0-9]{4}(-[0-9]+)?$`)
	// N6: the rebuild cache-bust token's 14-digit UTC timestamp.
	reCacheBust = regexp.MustCompile(`AI_TOOLS_CACHE_BUST=[0-9]{14}`)
	// N7: the worktree mutex owner file's "PID EPOCH" line. Defensive, not
	// load-bearing -- see goldencase_test.go's discussion of why the owner
	// file's bytes are never actually inlined into a golden.
	rePIDEpoch = regexp.MustCompile(`(?m)^[0-9]+ [0-9]+$`)
)

// normalizeText applies every named substitution. Order is load-bearing: the
// fixture root is a prefix of both the project and home paths, so it must be
// substituted LAST or it would swallow them and every golden would read
// <ROOT>/project instead of <PROJ>.
func normalizeText(s string, n normContext) string {
	s = strings.ReplaceAll(s, n.proj, "<PROJ>")
	s = strings.ReplaceAll(s, n.home, "<HOME>")
	if n.phash != "" {
		s = strings.ReplaceAll(s, n.phash, "<PHASH>")
	}
	s = reContainerTime.ReplaceAllString(s, "${1}-<TIME>${2}")
	s = reCacheBust.ReplaceAllString(s, "AI_TOOLS_CACHE_BUST=<TOKEN>")
	s = rePIDEpoch.ReplaceAllString(s, "<PID> <EPOCH>")
	if n.uid != "" {
		s = strings.ReplaceAll(s, "HOST_UID="+n.uid, "HOST_UID=<UID>")
	}
	if n.gid != "" {
		s = strings.ReplaceAll(s, "HOST_GID="+n.gid, "HOST_GID=<GID>")
	}
	if n.arch != "" {
		s = strings.ReplaceAll(s, "linux-"+n.arch, "linux-<ARCH>")
	}
	s = strings.ReplaceAll(s, n.root, "<ROOT>")
	return s
}
