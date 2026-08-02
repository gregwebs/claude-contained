package runtime

import "encoding/json"

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

// parseAppleImageID renders `container image inspect` output as one opaque
// identifier, or "" when it finds none.
//
// "" is not "the image is absent": probeImageID only calls this on a
// *successful* inspect, and classifies an empty answer as a fault. That is
// deliberate -- a shape this function cannot read is a defect here, and
// reporting it as absence would send the user to `--rebuild=full` for an image
// that is already built.
func parseAppleImageID(raw []byte) string {
	var top json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return ""
	}

	// An array is iterated element by element and anything else is one
	// document, matching parseAppleInspect's `inspect_objects`.
	docs := []json.RawMessage{top}
	if classify(top) == kindArray {
		if err := json.Unmarshal(top, &docs); err != nil {
			return ""
		}
	}

	for _, doc := range docs {
		if id := documentImageID(doc); id != "" {
			return id
		}
	}
	return ""
}

// documentImageID takes the first non-empty string any tolerated path yields.
// First-non-empty rather than first-present: a document carrying
// `"digest": ""` has told us nothing, and falling through to the next spelling
// is strictly better than returning an id that cannot name an image.
func documentImageID(doc json.RawMessage) string {
	for _, path := range appleImageIDPaths {
		cur := doc
		found := true
		for _, key := range path {
			if classify(cur) != kindObject {
				found = false
				break
			}
			obj, err := decodeObject(cur)
			if err != nil {
				found = false
				break
			}
			member, present := obj.lookup(key)
			if !present {
				found = false
				break
			}
			cur = member
		}
		if !found || classify(cur) != kindString {
			continue
		}
		var s string
		if err := json.Unmarshal(cur, &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}
