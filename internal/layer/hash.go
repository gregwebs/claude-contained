package layer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// schemeTag is the version of these enumeration rules. It exists so a future
// change to them deliberately invalidates every derived image instead of
// silently colliding with images hashed under the old rules -- a collision
// there means running a toolchain that is not the one the layer describes,
// which is the single failure this package exists to prevent.
const schemeTag = "claude-contained-layer\x00v1\x00"

// canonicalStream renders (baseImageID, the directory tree) as one canonical,
// domain-separated, length-unambiguous byte stream:
//
//	"claude-contained-layer\x00v1\x00"
//	"base\x00" <baseImageID> "\x00"
//	per entry, in flat sorted relative-path order:
//	    <relPath> "\x00" <kind> "\x00" <mode> "\x00" <size> "\x00" <contentHash> "\x00"
//
// Every field is present on every entry; the ones a kind has no answer for are
// empty. That is what makes the stream unambiguous: no reading of it can be
// confused about where one entry ends.
//
// This is a separate function from hashContext, and named, so hash_test.go can
// pin the format against a literal expected byte string a human can read and
// argue with. An expected *digest* could be neither written nor reviewed by
// hand -- and it is precisely because the stream is pinned this legibly that
// the golden files are allowed to normalize the digest away (see
// goldenfixture_test.go's reLayerHash).
//
// The Dockerfile is not hashed separately from the rest. The layer directory
// *is* the build context, so Dockerfile is enumerated like every other file;
// hashing it twice would add nothing and invite a bug where the two copies
// disagree about which bytes count.
//
// count and hashedBytes are returned, and nothing is refused. Size policy is
// the caller's: the layer directory is writable from inside the container, so a
// hard limit here would let a contained agent permanently break its own
// project's launcher, whose only escape would be --no-layer -- "a container
// that looks healthy while missing its toolchain", the exact outcome this
// design exists to prevent.
func canonicalStream(dir, baseImageID string) (stream []byte, count int, hashedBytes int64, err error) {
	entries, count, hashedBytes, err := enumerate(dir)
	if err != nil {
		return nil, 0, 0, err
	}

	// Flat sort of the collected relative paths, not walk order. WalkDir sorts
	// per directory (hierarchical), which disagrees with a flat sort whenever a
	// filename containing '.' sorts differently against a sibling directory
	// than their full paths would -- "a.txt" before "a/b" flatly, after it
	// hierarchically. The same trap goldenManifest documents. A flat sort is
	// the property we can state, test and reproduce.
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	var buf []byte
	buf = append(buf, schemeTag...)
	buf = append(buf, "base\x00"...)
	buf = append(buf, baseImageID...)
	buf = append(buf, 0)
	for _, e := range entries {
		for _, field := range []string{e.rel, e.kind, e.mode, e.size, e.content} {
			buf = append(buf, field...)
			buf = append(buf, 0)
		}
	}
	return buf, count, hashedBytes, nil
}

// hashContext is the SHA-256 of canonicalStream, hex-encoded in full. Resolve
// truncates it for the tag.
func hashContext(dir, baseImageID string) (string, int, int64, error) {
	stream, count, hashedBytes, err := canonicalStream(dir, baseImageID)
	if err != nil {
		return "", 0, 0, err
	}
	sum := sha256.Sum256(stream)
	return hex.EncodeToString(sum[:]), count, hashedBytes, nil
}

// entry is one enumerated path's contribution to the stream.
type entry struct {
	rel     string // slash-separated, relative to the layer directory
	kind    string // dir, file, symlink, other
	mode    string // files only; git's model, not the raw permission bits
	size    string // files and symlinks
	content string // files and symlinks
}

// Entry kinds. "other" covers fifos, sockets and devices, which contribute a
// line and are never opened: reading a fifo blocks forever, the same trap
// completeEnv documents for the project env file.
const (
	kindDir     = "dir"
	kindFile    = "file"
	kindSymlink = "symlink"
	kindOther   = "other"
)

func enumerate(dir string) ([]entry, int, int64, error) {
	var (
		entries     []entry
		hashedBytes int64
	)

	walkErr := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			// Unlike goldenManifest, an unreadable entry is fatal. Over-hashing
			// fails by rebuilding something that did not need it, which costs
			// time; under-hashing fails by running a stale toolchain, which is
			// the bug this feature must not have.
			return fmt.Errorf("reading tooling layer context %s: %w", path, err)
		}
		if path == dir {
			// The root's own line would be a constant, and its relative path is
			// "." which sorts ahead of everything and says nothing.
			return nil
		}

		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return fmt.Errorf("reading tooling layer context %s: %w", path, relErr)
		}
		rel = filepath.ToSlash(rel)

		// os.Lstat, never os.Stat: symlinks are not followed. Following them
		// would let the declared context reach files outside itself, so the
		// hash would depend on state the user never put in the layer; a symlink
		// loop would hang the walk; and neither runtime's symlink handling in a
		// build context is something the launcher should pretend to model.
		// Reading the entry's own metadata here rather than through fs.DirEntry
		// keeps that choice visible at the point it matters.
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fmt.Errorf("reading tooling layer context %s: %w", path, statErr)
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			// Hashed by its target *string*, not the target's contents. A
			// dangling symlink is therefore hashable and is not an error.
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return fmt.Errorf("reading tooling layer context %s: %w", path, linkErr)
			}
			sum := sha256.Sum256([]byte(target))
			entries = append(entries, entry{
				rel:     rel,
				kind:    kindSymlink,
				size:    strconv.Itoa(len(target)),
				content: hex.EncodeToString(sum[:]),
			})

		case info.IsDir():
			// Directories contribute a line so that adding an empty directory
			// changes the hash: a `COPY . .` makes an empty directory
			// observable in the image. Their modes are deliberately *not*
			// hashed -- mkdir applies the process umask, so hashing them would
			// give two developers on the same commit two different tags.
			entries = append(entries, entry{rel: rel, kind: kindDir})

		case info.Mode().IsRegular():
			sum, n, hashErr := hashFile(path)
			if hashErr != nil {
				return hashErr
			}
			hashedBytes += n
			entries = append(entries, entry{
				rel:     rel,
				kind:    kindFile,
				mode:    gitMode(info.Mode()),
				size:    strconv.FormatInt(info.Size(), 10),
				content: sum,
			})

		default:
			entries = append(entries, entry{rel: rel, kind: kindOther})
		}
		return nil
	})
	if walkErr != nil {
		return nil, 0, 0, walkErr
	}
	return entries, len(entries), hashedBytes, nil
}

// gitMode normalizes a file's permissions to the one bit git tracks: 0755 when
// any execute bit is set, 0644 otherwise.
//
// This is the least obvious line in the package and the reason it exists is
// portability, not tidiness. File permissions vary with the umask of whoever
// checked the repository out, so hashing info.Mode().Perm() would give two
// developers on the same commit two different tags -- and therefore two
// multi-minute builds of an identical image. Git tracks exactly one bit, so a
// checked-in layer hashes identically on every machine that checked it out,
// while `chmod +x build-helper.sh` still invalidates.
//
// The accepted cost, worth stating because it narrows "a changed layer always
// rebuilds": a `chmod 0640` genuinely changes what COPY puts in the image and
// does *not* change the tag.
func gitMode(mode os.FileMode) string {
	if mode.Perm()&0o111 != 0 {
		return "0755"
	}
	return "0644"
}

// hashFile streams the file rather than reading it whole: a layer may
// legitimately vendor a large tarball, and the size guard is a warning rather
// than a refusal, so this must stay bounded in memory.
//
// Nothing here interprets .dockerignore. Implementing dockerignore matching
// ("!" negation, "**", and the two runtimes' possibly-differing
// implementations) above internal/runtime would put build-context semantics in
// the launcher, where they could disagree with the runtime that applies them.
// Over-hashing's failure mode is a spurious rebuild, which is safe;
// under-hashing's is running a stale toolchain. A .dockerignore in the layer
// directory is therefore hashed like any other file.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("reading tooling layer context %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("reading tooling layer context %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
