package runtime

import (
	"encoding/json"
	"strings"
)

// appleImageIDPaths are the digest locations parseAppleImageID tolerates, in
// precedence order. The tolerance exists for the same reason parseAppleInspect's
// does (apple_inspect.go): Apple Containers has emitted more than one shape of
// inspect output, and a shape the launcher cannot read is a shape it would
// silently rebuild against forever.
//
// `configuration.descriptor.digest` is what `container` CLI 1.1.0 actually
// emits, confirmed by running `container image inspect` against a real image
// rather than by reading documentation. The shallower `descriptor.digest` and
// bare `digest` paths are kept as tolerance for other versions/shapes, each in
// the lower-case and capitalized spellings the known shapes use.
var appleImageIDPaths = [][]string{
	{"configuration", "descriptor", "digest"},
	{"Configuration", "Descriptor", "Digest"},
	{"descriptor", "digest"},
	{"Descriptor", "Digest"},
	{"digest"},
	{"Digest"},
}

var appleImageNamePaths = [][]string{
	{"configuration", "name"},
	{"Configuration", "Name"},
}

// parseAppleImageID renders `container image inspect` output as one opaque
// identifier, or "" when it finds none.
//
// "" is not "the image is absent": probeImageID only calls this on a
// *successful* inspect, and classifies an empty answer as a fault. That is
// deliberate -- a shape this function cannot read is a defect here, and
// reporting it as absence would send the user to `--rebuild=full` for an image
// that is already built.
func parseAppleImageID(raw []byte) string {
	docs, ok := appleImageDocuments(raw)
	if !ok {
		return ""
	}

	for _, doc := range docs {
		if id := documentImageID(doc); id != "" {
			return id
		}
	}
	return ""
}

// Apple 1.1 keeps the build tag in an OCI annotation after `image tag` has
// promoted the image and `image delete` has removed that source reference.
// `image inspect <old-build-tag>` still exits successfully by matching the
// annotation, so the current configuration name is the only evidence that the
// requested reference is actually absent. Older tolerated JSON shapes omit the
// name; those retain the existing digest-only behavior rather than becoming
// false absences.
func appleImageMatchesCurrentRef(raw []byte, ref string) bool {
	docs, ok := appleImageDocuments(raw)
	if !ok {
		return true
	}

	want := normalizeAppleImageRef(ref)
	sawName := false
	for _, doc := range docs {
		for _, path := range appleImageNamePaths {
			name := documentString(doc, path)
			if name == "" {
				continue
			}
			sawName = true
			if normalizeAppleImageRef(name) == want {
				return true
			}
		}
	}
	return !sawName
}

func normalizeAppleImageRef(ref string) string {
	return strings.TrimPrefix(ref, "docker.io/library/")
}

func appleImageDocuments(raw []byte) ([]json.RawMessage, bool) {
	var top json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, false
	}

	docs := []json.RawMessage{top}
	if classify(top) == kindArray {
		if err := json.Unmarshal(top, &docs); err != nil {
			return nil, false
		}
	}
	return docs, true
}

// documentImageID takes the first non-empty string any tolerated path yields.
// First-non-empty rather than first-present: a document carrying
// `"digest": ""` has told us nothing, and falling through to the next spelling
// is strictly better than returning an id that cannot name an image.
func documentImageID(doc json.RawMessage) string {
	for _, path := range appleImageIDPaths {
		if s := documentString(doc, path); s != "" {
			return s
		}
	}
	return ""
}

func documentString(doc json.RawMessage, path []string) string {
	cur := doc
	for _, key := range path {
		if classify(cur) != kindObject {
			return ""
		}
		obj, err := decodeObject(cur)
		if err != nil {
			return ""
		}
		member, present := obj.lookup(key)
		if !present {
			return ""
		}
		cur = member
	}
	if classify(cur) != kindString {
		return ""
	}
	var s string
	if err := json.Unmarshal(cur, &s); err != nil {
		return ""
	}
	return s
}
