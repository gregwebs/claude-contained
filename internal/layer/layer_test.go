package layer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"claude-contained/internal/plan"
)

// tagPattern is the derived tag's whole shape. The slug bound is 1..20
// characters because host.SanitizeFolderName truncates at 20, and the alphabet
// is [a-z0-9-] with an alphanumeric first character because that function trims
// a leading dash. It may legally *end* in a dash -- trimming happens before
// truncation -- which is why the separator before the digest can appear
// doubled.
var tagPattern = regexp.MustCompile(`^claude-contained-layer:[a-z0-9][a-z0-9-]{0,19}-[0-9a-f]{32}$`)

// layerDirFixture is the minimum a layer directory needs for Resolve to hash it.
func layerDirFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustResolve(t *testing.T, dir, projectDir string) Identity {
	t.Helper()
	id, err := Resolve(dir, projectDir, testBaseID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return id
}

func TestResolveNamesTheDerivedImage(t *testing.T) {
	dir := layerDirFixture(t)
	id := mustResolve(t, dir, "/work/my-app")

	if !tagPattern.MatchString(id.Tag) {
		t.Errorf("Tag = %q, does not match %s", id.Tag, tagPattern)
	}
	if len(id.Hash) != hashLen {
		t.Errorf("Hash = %q, want %d hex characters", id.Hash, hashLen)
	}
	if !strings.HasSuffix(id.Tag, "-"+id.Hash) {
		t.Errorf("Tag %q must end in the hash %q", id.Tag, id.Hash)
	}
	if id.Dir != dir {
		t.Errorf("Dir = %q, want %q", id.Dir, dir)
	}
	if want := filepath.Join(dir, "Dockerfile"); id.Dockerfile != want {
		t.Errorf("Dockerfile = %q, want %q", id.Dockerfile, want)
	}
	if id.FileCount != 1 {
		t.Errorf("FileCount = %d, want 1 (the Dockerfile)", id.FileCount)
	}
	if id.HashedBytes != int64(len("FROM scratch\n")) {
		t.Errorf("HashedBytes = %d, want %d", id.HashedBytes, len("FROM scratch\n"))
	}
}

func TestResolveSlugShapes(t *testing.T) {
	dir := layerDirFixture(t)

	cases := []struct {
		name       string
		projectDir string
		wantSlug   string
	}{
		{"an ugly basename sanitizes", "/work/My Project!", "my-project"},
		{
			// maxFolderNameLen is 20, not 40: a second truncation at any other
			// length here would be dead code.
			"a long basename truncates at twenty",
			"/work/abcdefghijklmnopqrstuvwxyz",
			"abcdefghijklmnopqrst",
		},
		{
			// The documented trap: dash trimming happens *before* truncation,
			// so truncating at 20 can legitimately leave a trailing dash, and
			// the tag then reads slug--hash.
			"truncation can leave a trailing dash",
			"/work/abcdefghijklmnopqrs-tuv",
			"abcdefghijklmnopqrs-",
		},
		{"an unnameable path falls back", "/", "root"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := mustResolve(t, dir, tc.projectDir)
			want := Repo + ":" + tc.wantSlug + "-" + id.Hash
			if id.Tag != want {
				t.Errorf("Tag = %q, want %q", id.Tag, want)
			}
			if !tagPattern.MatchString(id.Tag) {
				t.Errorf("Tag = %q, does not match %s", id.Tag, tagPattern)
			}
		})
	}
}

// projectDir is handed to host.SanitizeFolderName whole, not through
// filepath.Base. That function applies its own basename(1) reproduction, which
// diverges from filepath.Base on exactly these inputs -- wrapping it would
// re-introduce the divergence its comment exists to prevent.
func TestResolvePassesTheProjectPathWhole(t *testing.T) {
	dir := layerDirFixture(t)

	withSlash := mustResolve(t, dir, "/work/my-app/")
	withoutSlash := mustResolve(t, dir, "/work/my-app")
	if withSlash.Tag != withoutSlash.Tag {
		t.Errorf("a trailing slash changed the tag: %q vs %q", withSlash.Tag, withoutSlash.Tag)
	}
}

// The tag's project prefix is decorative; the hash is not a function of it.
func TestResolveHashIgnoresTheProjectDirectory(t *testing.T) {
	dir := layerDirFixture(t)

	if a, b := mustResolve(t, dir, "/work/alpha"), mustResolve(t, dir, "/work/beta"); a.Hash != b.Hash {
		t.Errorf("the project directory must not enter the hash: %s vs %s", a.Hash, b.Hash)
	}
}

// USAGE.md carries the copy-pasteable preamble a layer author starts from, and
// the launcher overrides that same ARG by name. If the documented text and
// these constants drift, every documented layer draws an unconsumed-build-arg
// warning and the ARG default stops matching the base image tag.
//
// This covers the *documentation* half of checklist item 7 and explicitly
// nothing else: whether a builder accepts the preamble needs a real builder and
// a real base image, which is manual verification #18.
func TestDocumentedLayerPreambleMatchesTheConstants(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "USAGE.md"))
	if err != nil {
		t.Fatalf("reading USAGE.md: %v", err)
	}
	usage := string(raw)

	const heading = "## Tooling Layers"
	start := strings.Index(usage, heading)
	if start < 0 {
		t.Fatalf("USAGE.md has no %q section", heading)
	}
	section := usage[start+len(heading):]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}

	const fence = "```dockerfile"
	openIdx := strings.Index(section, fence)
	if openIdx < 0 {
		t.Fatalf("the %q section has no fenced dockerfile block", heading)
	}
	block := section[openIdx+len(fence):]
	if closeIdx := strings.Index(block, "```"); closeIdx >= 0 {
		block = block[:closeIdx]
	}

	for _, want := range []string{
		"ARG " + BaseImageArg + "=" + plan.Image,
		"FROM ${" + BaseImageArg + "}",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the documented preamble does not contain %q:\n%s", want, block)
		}
	}
}
