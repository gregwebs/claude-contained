package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DefaultCommandInjectionJSON is the documented copy-pasteable default users
// create at <project-dir>/.claude-contained/commands.json: it restores the
// pre-#22 --add-dir capability for claude and codex. The launcher never
// writes it (Decision B, #39, no auto-seed) -- it exists here as a doc/test
// constant only, shared with USAGE.md's example and with tests so the two
// never drift.
const DefaultCommandInjectionJSON = `{
  "claude": { "mountFlag": "--add-dir" },
  "codex":  { "mountFlag": "--add-dir" }
}
`

// commandInjectionEntry is decoded with a pointer MountFlag so a missing key
// (nil) is distinguishable from an explicit empty string -- both are errors,
// but the pointer is what lets ParseCommandInjection tell them apart in the
// message. Unknown per-entry keys are tolerated (no DisallowUnknownFields):
// this file is user-created and forward compatibility with a future config
// key matters more than catching a typo in "mountFlag" here.
type commandInjectionEntry struct {
	MountFlag *string `json:"mountFlag"`
}

// ParseCommandInjection parses commands.json into token->mountFlag. It is a
// hard error when the JSON is malformed or any entry's mountFlag is missing,
// empty, or not a string -- the file is user-created and read-only inside the
// container (Decision A, #39), so a parse failure is a user mistake, not
// input to tolerate silently. An empty or whitespace-only document, or "{}",
// is a valid empty map (no injection configured).
func ParseCommandInjection(data []byte) (map[string]string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]string{}, nil
	}

	var raw map[string]commandInjectionEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	injection := make(map[string]string, len(raw))
	for command, entry := range raw {
		if entry.MountFlag == nil || *entry.MountFlag == "" {
			return nil, fmt.Errorf("command %q: mountFlag must be a non-empty string", command)
		}
		injection[command] = *entry.MountFlag
	}
	return injection, nil
}
