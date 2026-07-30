package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// This file reproduces the jq filter at claude-contained:447-465, which is the
// bash launcher's way of reading a container's environment out of
// `container inspect`. Apple Containers has emitted more than one shape of
// inspect output, so the filter tolerates four locations and four value shapes,
// and the port has to tolerate the same ones -- environment inspection is the
// portable source of truth for Zellij discovery, so a shape bash reads and Go
// does not would mean the two disagree about which sessions exist.
//
// Every rule below was established by *running* jq 1.8.1 against the extracted
// filter, not by reading it. The golden table in apple_inspect_test.go is that
// output. Regenerate it rather than reasoning about it:
//
//	sed -n '448,464p' claude-contained > /tmp/filter.jq
//	printf '%s' '<json>' | jq -r -f /tmp/filter.jq
//
// Go needs no jq at all, which is a small improvement: on a host without jq the
// bash Apple path silently discovers no sessions.

// appleEnvPaths are the four locations the filter tolerates, in its order.
// All four are read and their lines *concatenated* -- the filter builds a
// four-element array and iterates it, so a document carrying two of them yields
// both. "First match wins" is the natural-looking mistake and it is wrong.
var appleEnvPaths = [][]string{
	{"configuration", "initProcess", "environment"},
	{"Configuration", "InitProcess", "Environment"},
	{"config", "env"},
	{"Config", "Env"},
}

// parseAppleInspect renders `container inspect` output as the KEY=VALUE lines
// bash would have read.
//
// The final split is what keeps the two runtimes in agreement: `jq -r` prints a
// value containing a newline as *several* output lines, and bash reads them with
// `while IFS= read -r`, so Docker's `{{println .}}` output and this must produce
// the same line sequence for the same logical environment.
func parseAppleInspect(raw []byte) []string {
	return splitEnvLines(strings.Join(appleInspectLines(raw), "\n"))
}

func appleInspectLines(raw []byte) []string {
	var top json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		// A jq parse error prints nothing, and bash's `2>/dev/null || true`
		// turns that into "no environment".
		return nil
	}

	// `def inspect_objects: if type == "array" then .[] else . end` -- an array is
	// iterated element by element, anything else is one document.
	docs := []json.RawMessage{top}
	if classify(top) == kindArray {
		if err := json.Unmarshal(top, &docs); err != nil {
			return nil
		}
	}

	var out []string
	for _, doc := range docs {
		lines, ok := documentLines(doc)
		if !ok {
			// A traversal error aborts the whole jq program, so no later document
			// contributes anything -- but bash keeps the partial stdout earlier
			// documents already produced.
			return out
		}
		out = append(out, lines...)
	}
	return out
}

// documentLines renders one document, or reports that jq would have errored.
//
// The four locations are gathered *before* any of them is rendered, because the
// filter builds the four-element array first. That is why one bad path discards
// the whole document, including the paths that succeeded.
func documentLines(doc json.RawMessage) ([]string, bool) {
	values := make([]json.RawMessage, 0, len(appleEnvPaths))
	for _, path := range appleEnvPaths {
		v, ok := lookupPath(doc, path)
		if !ok {
			return nil, false
		}
		values = append(values, v)
	}

	var out []string
	for _, v := range values {
		out = append(out, envLines(v)...)
	}
	return out, true
}

// lookupPath walks one location. ok=false means jq would have raised an error
// and aborted.
//
// The filter's `?` guards only the *final* component, so indexing a non-null,
// non-object value is fatal everywhere else: `{"configuration":"x"}` dies on
// `.initProcess`, while `{"config":"x"}` is merely empty because `.env?` is
// guarded. Indexing null is never an error and yields null.
func lookupPath(doc json.RawMessage, path []string) (json.RawMessage, bool) {
	cur := doc
	for i, key := range path {
		switch classify(cur) {
		case kindNull:
			return nil, true
		case kindObject:
			obj, err := decodeObject(cur)
			if err != nil {
				return nil, false
			}
			member, present := obj.lookup(key)
			if !present {
				return nil, true
			}
			cur = member
		default:
			if i == len(path)-1 {
				return nil, true
			}
			return nil, false
		}
	}
	return cur, true
}

// envLines is the filter's `envlines`. A value that is neither an array nor an
// object -- including null and an absent path -- yields nothing rather than an
// error.
func envLines(v json.RawMessage) []string {
	switch classify(v) {
	case kindArray:
		var elems []json.RawMessage
		if err := json.Unmarshal(v, &elems); err != nil {
			return nil
		}
		var out []string
		for _, elem := range elems {
			out = append(out, elementLines(elem)...)
		}
		return out
	case kindObject:
		return memberLines(v)
	}
	return nil
}

// elementLines renders one array element: a string verbatim, an object having
// *both* `name` and `value` as `name=value`, any other object member by member,
// and anything else not at all.
func elementLines(elem json.RawMessage) []string {
	switch classify(elem) {
	case kindString:
		var s string
		if err := json.Unmarshal(elem, &s); err != nil {
			return nil
		}
		return []string{s}
	case kindObject:
		obj, err := decodeObject(elem)
		if err != nil {
			return nil
		}
		// has("name") and has("value") -- presence, not non-emptiness, so a null
		// value still takes this arm and renders as `name=null`. An object with
		// `name` but no `value` falls through and renders as `name=A`.
		name, hasName := obj.lookup("name")
		value, hasValue := obj.lookup("value")
		if hasName && hasValue {
			return []string{renderValue(name) + "=" + renderValue(value)}
		}
		return obj.lines()
	}
	return nil
}

func memberLines(v json.RawMessage) []string {
	obj, err := decodeObject(v)
	if err != nil {
		return nil
	}
	return obj.lines()
}

// renderValue is jq's string interpolation: a string yields its unquoted
// contents, anything else its JSON form.
//
// jq's number canonicalization is deliberately not reproduced -- it would render
// 1e3 as 1E+3. No container runtime emits a numeric environment value, and
// reimplementing jq's formatter would be pure liability.
func renderValue(v json.RawMessage) string {
	if classify(v) == kindString {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			return s
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, v); err != nil {
		return string(v)
	}
	return compact.String()
}

// jsonObject is a JSON object with member order preserved. jq's to_entries emits
// pairs in document order and two of the tolerated value shapes are rendered
// pair by pair, so a map[string]any -- whose Go iteration order is randomized --
// would emit a different line order on every run, making the test flake rather
// than fail.
type jsonObject []jsonMember

type jsonMember struct {
	key string
	val json.RawMessage
}

var errNotObject = errors.New("not a JSON object")

// UnmarshalJSON keeps the first occurrence's position with the last occurrence's
// value, which is what jq does: {"A":1,"B":2,"A":3} yields A=3 then B=2.
func (o *jsonObject) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return errNotObject
	}

	*o = nil
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return errNotObject
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return err
		}
		if i := o.indexOf(key); i >= 0 {
			(*o)[i].val = val
			continue
		}
		*o = append(*o, jsonMember{key: key, val: val})
	}
	_, err = dec.Token() // the closing brace
	return err
}

func (o jsonObject) indexOf(key string) int {
	for i, m := range o {
		if m.key == key {
			return i
		}
	}
	return -1
}

func (o jsonObject) lookup(key string) (json.RawMessage, bool) {
	if i := o.indexOf(key); i >= 0 {
		return o[i].val, true
	}
	return nil, false
}

func (o jsonObject) lines() []string {
	var out []string
	for _, m := range o {
		out = append(out, m.key+"="+renderValue(m.val))
	}
	return out
}

func decodeObject(raw json.RawMessage) (jsonObject, error) {
	var obj jsonObject
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// jsonKind is the JSON type of a raw value, which is all the filter's `type ==`
// tests need. Determined from the first non-space byte rather than by decoding,
// because several arms only need to know the shape.
type jsonKind int

const (
	kindOther jsonKind = iota
	kindNull
	kindObject
	kindArray
	kindString
)

func classify(raw json.RawMessage) jsonKind {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return kindObject
		case '[':
			return kindArray
		case '"':
			return kindString
		case 'n':
			return kindNull
		default:
			return kindOther
		}
	}
	return kindNull
}
