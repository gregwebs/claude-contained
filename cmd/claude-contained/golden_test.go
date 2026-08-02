package main

// golden_test.go is the driver: for every case in goldencase_test.go,
// across the three runtime/platform configurations, it builds an isolated fixture
// (goldenfixture_test.go), drives runWith directly -- in-process, with `plat`
// injected -- through an injected runner and a swapped replaceProcess,
// and compares the normalized, five-section result against
// testdata/golden/<tree>/<slug>.txt.
//
// Injecting the platform is the whole point: internal/runtime.Select only chooses Apple Containers
// when plat == Darwin (runtime.go:222-223), and ValidateSelection refuses an
// apple selection off Darwin (runtime.go:271-275) -- so a subprocess built
// from the real GOOS could never select Apple on Linux CI. Injecting plat
// makes all three configurations reachable from either host.

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"

	"claude-contained/internal/host"
	"claude-contained/internal/runtime"
)

var updateGolden = flag.Bool("update", false, "regenerate golden test data (cmd/claude-contained/testdata/golden)")

// goldenTreeConfig is one of the three configurations: apple-darwin,
// docker-darwin, docker-linux. Selected purely by the injected plat and
// (for the Docker trees) CLAUDE_CONTAINED_RUNTIME=docker -- never a flag,
// which would perturb every case's own argv.
type goldenTreeConfig struct {
	tree      string
	plat      runtime.Platform
	dockerEnv bool
	// sshAgent seeds SSH_AUTH_SOCK. Docker reads the real agent socket only
	// off Darwin (internal/runtime/docker.go's sshArgs); the Darwin arm uses a
	// fixed bridged path and ignores the variable entirely.
	sshAgent bool
}

var goldenTrees = []goldenTreeConfig{
	{tree: "apple-darwin", plat: runtime.Darwin, dockerEnv: false},
	{tree: "docker-darwin", plat: runtime.Darwin, dockerEnv: true},
	{tree: "docker-linux", plat: runtime.Linux, dockerEnv: true, sshAgent: true},
}

func TestGolden(t *testing.T) {
	for _, tc := range goldenCases {
		tc := tc
		for _, gc := range goldenTrees {
			gc := gc
			t.Run(gc.tree+"/"+tc.Slug, func(t *testing.T) {
				runGoldenCase(t, tc, gc)
			})
		}
	}
}

func runGoldenCase(t *testing.T, tc goldenCase, gc goldenTreeConfig) {
	// A host skip, not a tree skip: case 49's node_modules overlay
	// gates on runtime.GOOS at compile time (probe.go:70), which does not
	// vary with the injected plat. Skipped identically in all three trees
	// when this test binary was not built for HostGOOS.
	if tc.HostGOOS != "" && goruntime.GOOS != tc.HostGOOS {
		t.Skipf("%s is %s-only by construction: probe.go:70 gates the node_modules overlay on the compile-time GOOS, which the injected plat does not change", tc.Slug, tc.HostGOOS)
	}

	// The controlled environment: every variable the launcher reads
	// must be explicit, nothing inherited from the developer's shell.
	clearEnv(t, launcherEnvVars...)

	fx := newGoldenFixture(t)
	t.Setenv("HOME", fx.home)
	t.Setenv("PATH", fx.stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TZ", "UTC")
	t.Setenv("GOLDEN_LIST_OUTPUT", fx.stubList)
	t.Setenv("GOLDEN_INSPECT_DIR", fx.stubInspectDir)
	t.Setenv("GOLDEN_IMAGE_ID_DIR", fx.stubImageIDDir)

	if gc.dockerEnv {
		t.Setenv("CLAUDE_CONTAINED_RUNTIME", "docker")
	}
	if gc.sshAgent {
		// Only case 08 (-S/--ssh) renders anything from this; every other
		// case ignores it, so it is safe to set unconditionally rather than
		// threading a per-case flag through the table.
		t.Setenv("SSH_AUTH_SOCK", filepath.Join(fx.root, "ssh-agent.sock"))
	}

	var extras goldenExtras
	if tc.Setup != nil {
		extras = tc.Setup(t, fx.proj, fx.home)
	}
	for k, v := range extras.Env {
		t.Setenv(k, v)
	}
	if len(extras.ListOutput) > 0 {
		mustWriteFile(t, fx.stubList, strings.Join(extras.ListOutput, "\n")+"\n")
	}
	for name, lines := range extras.InspectEnv {
		mustWriteFile(t, filepath.Join(fx.stubInspectDir, name+".env"), strings.Join(lines, "\n")+"\n")
	}
	for ref, id := range extras.ImageIDs {
		mustWriteFile(t, filepath.Join(fx.stubImageIDDir, goldenImageIDKey(ref)+".id"), id+"\n")
	}

	// The terminal answer is a package-level var for exactly this: the driver
	// hands runWith a strings.Reader, so a case that needs a prompt gated on
	// having a terminal has no other way to reach it. Swapped and restored the
	// same way replaceProcess is, below.
	if tc.Terminal {
		origTerminal := isTerminal
		isTerminal = func(io.Reader) bool { return true }
		t.Cleanup(func() { isTerminal = origTerminal })
	}

	args := append([]string{"claude-contained"}, tc.Args(fx.proj, fx.home)...)

	baseline := fullManifest(fx.home, fx.proj)

	var log strings.Builder
	captureRun := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		// The mid-run window: copy every declared snapshot path that
		// exists right now, before returning -- the only point at which the
		// worktree lock is observable, since it is released again in the
		// launcher's own deferred cleanup immediately after this returns.
		for _, p := range extras.Snapshot {
			if info, err := os.Stat(p); err == nil {
				if data, err := os.ReadFile(p); err == nil {
					_ = os.WriteFile(p+".mid-run-snapshot", data, info.Mode().Perm())
				}
			}
		}
		log.WriteString(renderInvocation(argv))
		return 0
	}

	origExec := replaceProcess
	replaceProcess = func(argv []string) error {
		log.WriteString(renderInvocation(argv))
		return nil
	}
	t.Cleanup(func() { replaceProcess = origExec })

	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader(tc.Stdin)
	exitCode := runWith(captureRun, gc.plat, args, stdin, &stdout, &stderr)

	post := fullManifest(fx.home, fx.proj)
	argvLog := log.String()

	// Liveness guard 1: a case that produced literally nothing distinguishable from
	// doing nothing is a harness error, not a pass.
	if resultIsEmpty(stdout.String(), stderr.String(), argvLog, exitCode, baseline, post) {
		t.Fatalf("case %s/%s produced no observable result at all (empty stdout/stderr, exit 0, "+
			"no runtime args, unchanged filesystem): this case didn't reach the behavior it claims to exercise",
			gc.tree, tc.Slug)
	}

	// Liveness guard 2, bidirectional.
	if tc.NoRuntimeArgs && argvLog != "" {
		t.Fatalf("case %s declares NoRuntimeArgs but built some: the case declares it exits before "+
			"building runtime arguments, but built some:\n%s", tc.Slug, argvLog)
	}
	if !tc.NoRuntimeArgs && argvLog == "" {
		t.Fatalf("case %s expects runtime args but none were captured: the case never reached the run path", tc.Slug)
	}

	norm := normContext{
		proj: fx.proj, home: fx.home, root: fx.root,
		arch: host.Probe().Arch,
		uid:  strconv.Itoa(os.Getuid()),
		gid:  strconv.Itoa(os.Getgid()),
	}
	if cTarget := extractDashC(args); cTarget != "" {
		norm.phash = host.PathHash8(host.ResolvePath(cTarget))
	}

	rendered := renderGoldenFile(tc, gc, exitCode, argvLog, stdout.String(), stderr.String(), post, norm)

	goldenPath := filepath.Join("testdata", "golden", gc.tree, tc.Slug+".txt")
	compareOrUpdateGolden(t, goldenPath, rendered)
}

// fullManifest is baseline/post's shared shape: HOME's entries, then PROJ's.
func fullManifest(home, proj string) []string {
	return append(goldenManifest(home, "HOME"), goldenManifest(proj, "PROJ")...)
}

// resultIsEmpty is a direct port of the retired harness.sh's side_is_empty.
func resultIsEmpty(stdout, stderr, argvLog string, exitCode int, baseline, post []string) bool {
	if stdout != "" || stderr != "" || argvLog != "" || exitCode != 0 {
		return false
	}
	if len(baseline) != len(post) {
		return false
	}
	for i := range baseline {
		if baseline[i] != post[i] {
			return false
		}
	}
	return true
}

// goldenImageIDKey turns an image reference into the id file's basename. It
// mirrors the `tr ':/' '__'` in goldenImageIDArm exactly; the two must agree or
// every layer case reads as "no such image".
func goldenImageIDKey(ref string) string {
	return strings.NewReplacer(":", "_", "/", "_").Replace(ref)
}

// extractDashC finds -C's value in a rendered argv, for N4/PHASH. Cases with
// no -C at all (attach, rebuild) return "".
func extractDashC(args []string) string {
	for i, a := range args {
		if a == "-C" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// renderInvocation is the retired stub's log line shape (lib/isolate.sh:88-92),
// kept because it is compact and reviewable -- not because anything now
// compares against it. argv[0] and argv[1] are the injected runner's or
// replaceProcess's own bin and subcommand.
func renderInvocation(argv []string) string {
	var b strings.Builder
	self, sub := "", ""
	if len(argv) > 0 {
		self = argv[0]
	}
	if len(argv) > 1 {
		sub = argv[1]
	}
	fmt.Fprintf(&b, "== %s %s ==\n", self, sub)
	var rest []string
	if len(argv) > 2 {
		rest = argv[2:]
	}
	for _, a := range rest {
		b.WriteString(a)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}

// renderGoldenFile assembles the five fixed sections, each
// normalized independently, in a fixed order, with "(empty)" standing in for
// a section with no content so a truncated file can never read as a
// legitimately empty one.
func renderGoldenFile(tc goldenCase, gc goldenTreeConfig, exitCode int, argvLog, stdout, stderr string, manifest []string, norm normContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# case: %s\n", tc.Slug)
	fmt.Fprintf(&b, "# desc: %s\n", tc.Desc)
	fmt.Fprintf(&b, "# config: %s\n", gc.tree)

	fmt.Fprintf(&b, "--- exit ---\n%d\n", exitCode)

	section := func(name, content string) {
		fmt.Fprintf(&b, "--- %s ---\n", name)
		content = normalizeText(content, norm)
		if content == "" {
			b.WriteString("(empty)\n")
			return
		}
		b.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			b.WriteByte('\n')
		}
	}

	section("runtime-args", argvLog)
	section("stdout", stdout)
	section("stderr", stderr)
	section("filesystem", strings.Join(manifest, "\n"))

	return b.String()
}

// compareOrUpdateGolden is D7: -update regenerates and prints a per-file diff
// of what it changed; ordinary runs fail loudly when the golden is missing
// or diverges.
func compareOrUpdateGolden(t *testing.T, path, rendered string) {
	t.Helper()
	existing, err := os.ReadFile(path)

	if *updateGolden {
		if err == nil && string(existing) == rendered {
			return
		}
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
			t.Fatal(mkErr)
		}
		if writeErr := os.WriteFile(path, []byte(rendered), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
		t.Logf("updated %s:\n%s", path, lineDiff(string(existing), rendered))
		return
	}

	if err != nil {
		t.Fatalf("golden file missing: %s (run `go test ./cmd/claude-contained -run TestGolden -update`)\n\n--- got ---\n%s", path, rendered)
	}
	if string(existing) != rendered {
		t.Fatalf("golden mismatch: %s\n%s", path, lineDiff(string(existing), rendered))
	}
}

// lineDiff is a minimal LCS-based line diff -- not byte-identical to `diff -u`
// output, but the same information: which lines were removed and which were
// added, for a developer reviewing an -update run.
func lineDiff(oldText, newText string) string {
	oldLines := splitLinesKeep(oldText)
	newLines := splitLinesKeep(newText)

	n, m := len(oldLines), len(newLines)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var b strings.Builder
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			fmt.Fprintf(&b, "-%s\n", oldLines[i])
			i++
		default:
			fmt.Fprintf(&b, "+%s\n", newLines[j])
			j++
		}
	}
	for ; i < n; i++ {
		fmt.Fprintf(&b, "-%s\n", oldLines[i])
	}
	for ; j < m; j++ {
		fmt.Fprintf(&b, "+%s\n", newLines[j])
	}
	return b.String()
}

func splitLinesKeep(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
