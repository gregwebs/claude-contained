// Package layer turns a project's tooling layer directory into the identity of
// the derived image the launcher must run.
//
// Identity is a content hash rather than a recorded state file: the tag is the
// staleness check, so there is nothing to forget to write and nothing that can
// disagree with the image store. See docs/adr/0006-tooling-layers.md.
//
// This package imports only the standard library and internal/host. It
// deliberately does *not* import internal/runtime -- orchestration (probing the
// runtime, prompting, building) lives in cmd/claude-contained/layer.go, exactly
// as rebuild orchestration lives in cmd/claude-contained/rebuild.go. That keeps
// the hash trivially testable with no seam of any kind.
package layer

import (
	"path/filepath"

	"claude-contained/internal/host"
)

// Repo is the derived images' repository name, separate from plan.Image's so
// that `docker image ls claude-contained` stays readable and
// `docker image ls claude-contained-layer` is the cleanup handle the spec asks
// for.
//
// Rejected: a bare `claude-contained-layer:<hash>` tag. Purer as a content
// address, but it gives an image listing no per-project handle, which is
// exactly the property the separate repository name exists to provide.
const Repo = "claude-contained-layer"

// BaseImageArg is the build argument a layer declares as
// `ARG BASE_IMAGE=claude-contained:latest` and consumes as `FROM ${BASE_IMAGE}`.
// The launcher overrides it with a runtime-selected builder reference while
// separately hashing the base image's stable identity; the default in the
// Dockerfile is what keeps the layer buildable by hand and by a devcontainer.
const BaseImageArg = "BASE_IMAGE"

// Build labels, namespaced like the Zellij labels (internal/zellij). They are
// provenance for a human running `docker image inspect` and are never read
// back: identity and staleness ride entirely on the tag, the rule ADR-0002
// established for Zellij discovery. Emitted on Docker only -- see
// runtime.BuildSpec.Labels.
const (
	LabelLayer      = "claude-contained.layer"
	LabelProject    = "claude-contained.layer.project"
	LabelDockerfile = "claude-contained.layer.dockerfile"
	LabelBase       = "claude-contained.layer.base"
)

// hashLen is how much of the SHA-256 the tag carries, in hex characters.
// host.PathHash8 is the precedent for truncating a digest at all; it takes 8,
// this takes 32 (128 bits) because that digest only names a Zellij session
// while this one decides whether to skip a build -- and "collision" here means
// silently running the wrong toolchain.
const hashLen = 32

// Identity is everything a caller needs about a resolved, hashed layer.
type Identity struct {
	// Dir is the layer directory, which is also the build context.
	Dir string
	// Dockerfile is the build recipe inside Dir.
	Dockerfile string
	// Tag is Repo + ":" + slug + "-" + Hash.
	Tag string
	// Hash is the truncated content digest, hashLen hex characters.
	Hash string
	// FileCount and HashedBytes let the caller warn about an oversized context
	// without this package owning a size policy. Nothing here refuses anything.
	FileCount   int
	HashedBytes int64
}

// Resolve hashes dir against baseImageID and names the derived image.
//
// The *hash* covers exactly the three things the ticket names: the base image's
// resolved ID, the Dockerfile, and the build-context files. The *tag* is that
// hash plus a readable project prefix, which is decorative and deliberately not
// part of the hash. The consequence -- two projects with byte-identical layers
// on the same base build twice -- is what the spec already describes ("one per
// project per layer version per base version") and is what makes per-project
// cleanup possible.
func Resolve(dir, projectDir, baseImageID string) (Identity, error) {
	digest, count, hashedBytes, err := hashContext(dir, baseImageID)
	if err != nil {
		return Identity{}, err
	}
	hash := digest[:hashLen]

	// projectDir is passed whole, not through filepath.Base.
	// host.SanitizeFolderName applies its own baseName, which deliberately
	// reproduces basename(1) rather than filepath.Base -- they differ on the
	// inputs that matter, and wrapping it would re-introduce the divergence
	// that function's comment exists to prevent. It also truncates at 20 on its
	// own, so there is no second truncation here, and its output can legally end
	// in a dash (trimming happens before truncation), which makes `slug--hash`
	// a tag every assertion must tolerate.
	slug := host.SanitizeFolderName(projectDir)

	return Identity{
		Dir:         dir,
		Dockerfile:  filepath.Join(dir, "Dockerfile"),
		Tag:         Repo + ":" + slug + "-" + hash,
		Hash:        hash,
		FileCount:   count,
		HashedBytes: hashedBytes,
	}, nil
}
