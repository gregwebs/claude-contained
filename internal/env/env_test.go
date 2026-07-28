package env

import (
	"reflect"
	"strings"
	"testing"
)

func pairs(s *Store) []Pair { return s.Pairs() }

func mustSet(t *testing.T, s *Store, assignment, source string, o Origin) {
	t.Helper()
	if err := s.Set(assignment, source, o); err != nil {
		t.Fatalf("Set(%q): unexpected error: %v", assignment, err)
	}
}

// --- ordering and deduplication -------------------------------------------

func TestSetKeepsInsertionOrder(t *testing.T) {
	s := New()
	mustSet(t, s, "A=1", "--env", Flag)
	mustSet(t, s, "B=2", "--env", Flag)
	mustSet(t, s, "C=3", "--env", Flag)

	want := []Pair{{"A", "1"}, {"B", "2"}, {"C", "3"}}
	if got := pairs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Pairs() = %#v, want %#v", got, want)
	}
}

// A repeated key is emitted once, with the last value -- and critically it keeps
// its *original* position, because bash replaces in place rather than appending.
func TestSetReplacesInPlace(t *testing.T) {
	s := New()
	mustSet(t, s, "A=1", "--env", Flag)
	mustSet(t, s, "B=2", "--env", Flag)
	mustSet(t, s, "A=second", "--env", Flag)

	want := []Pair{{"A", "second"}, {"B", "2"}}
	if got := pairs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Pairs() = %#v, want %#v", got, want)
	}
}

func TestSetPreservesSpacesAndEquals(t *testing.T) {
	s := New()
	mustSet(t, s, "GREETING=hello world", "--env", Flag)
	mustSet(t, s, "CONN=k=v;x=y", "--env", Flag)
	mustSet(t, s, "EMPTY=", "--env", Flag)

	want := []Pair{{"GREETING", "hello world"}, {"CONN", "k=v;x=y"}, {"EMPTY", ""}}
	if got := pairs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Pairs() = %#v, want %#v", got, want)
	}
}

// --- validation ------------------------------------------------------------

func TestValidationMessages(t *testing.T) {
	cases := []struct {
		name       string
		assignment string
		source     string
		origin     Origin
		want       string
	}{
		{
			name: "no equals sign", assignment: "JUSTAKEY", source: "--env", origin: Flag,
			want: "error: --env: expected KEY=VALUE, got 'JUSTAKEY'",
		},
		{
			name: "empty assignment", assignment: "", source: "--env", origin: Flag,
			want: "error: --env: expected KEY=VALUE, got ''",
		},
		{
			name: "leading digit", assignment: "9BAD=x", source: "--env", origin: Flag,
			want: "error: --env: not a valid environment variable name: '9BAD'",
		},
		{
			name: "empty key", assignment: "=x", source: "--env", origin: Flag,
			want: "error: --env: not a valid environment variable name: ''",
		},
		{
			name: "dash in key", assignment: "A-B=x", source: "--env", origin: Flag,
			want: "error: --env: not a valid environment variable name: 'A-B'",
		},
		{
			name: "space in key", assignment: "A B=x", source: "--env", origin: Flag,
			want: "error: --env: not a valid environment variable name: 'A B'",
		},
		{
			// The message names the product, not the program, so it is identical
			// for both container runtimes.
			name: "always-reserved exact", assignment: "STAY_ROOT=1", source: "--env", origin: Flag,
			want: "error: --env: STAY_ROOT is reserved by claude-contained and cannot be set",
		},
		{
			name: "always-reserved prefix", assignment: "HOST_ANYTHING=1", source: "--env", origin: Flag,
			want: "error: --env: HOST_ANYTHING is reserved by claude-contained and cannot be set",
		},
		{
			name:       "file-only reserved, from the file",
			assignment: "LD_PRELOAD=/tmp/evil.so", source: ".claude-contained/env:1", origin: File,
			want: "error: .claude-contained/env:1: LD_PRELOAD cannot be set from a project env file\n" +
				"       It is read by the sandbox wrapper itself; pass -e LD_PRELOAD=... if you mean it.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New()
			err := s.Set(tc.assignment, tc.source, tc.origin)
			if err == nil {
				t.Fatalf("Set(%q) succeeded, want rejection", tc.assignment)
			}
			if got := err.Error(); got != tc.want {
				t.Errorf("message =\n%q\nwant\n%q", got, tc.want)
			}
			if len(pairs(s)) != 0 {
				t.Error("a rejected assignment must not be recorded")
			}
		})
	}
}

// Every always-reserved key, from either source. These are levers on the
// container's own guarantees, so the list is asserted explicitly rather than
// spot-checked.
func TestReservedAlways(t *testing.T) {
	keys := []string{
		"STAY_ROOT", "SSH_AUTH_SOCK", "GIT_PROTECT_DIRS", "HOME", "PATH", "JAVA_HOME",
		"HOST_HOME", "HOST_", "SRT_DISABLE", "SRT_", "CLAUDE_CONTAINED_ZELLIJ", "CLAUDE_CONTAINED_",
	}
	for _, key := range keys {
		for _, origin := range []Origin{Flag, File, Builtin} {
			if err := New().Set(key+"=x", "src", origin); err == nil {
				t.Errorf("%s from origin %v was accepted, want rejection", key, origin)
			}
		}
	}
}

// The file-only tier is the whole point of having two tiers: an agent can write
// the file, so loader injection is refused there but fine by hand.
func TestReservedInFileOnly(t *testing.T) {
	for _, key := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "NODE_OPTIONS"} {
		if err := New().Set(key+"=x", ".claude-contained/env:1", File); err == nil {
			t.Errorf("%s from the file was accepted, want rejection", key)
		}
		if err := New().Set(key+"=x", "--env", Flag); err != nil {
			t.Errorf("%s from a flag was rejected (%v), want acceptance", key, err)
		}
	}
}

func TestOrdinaryKeysAreNotReserved(t *testing.T) {
	// DISPLAY is deliberately allowed: the entrypoint only defaults it when empty.
	for _, key := range []string{"FOO", "DISPLAY", "HOSTNAME", "SRTP", "CLAUDE_CODE"} {
		if err := New().Set(key+"=x", "--env", Flag); err != nil {
			t.Errorf("%s was rejected (%v), want acceptance", key, err)
		}
	}
}

// --- Default ---------------------------------------------------------------

func TestDefaultYieldsToAnExistingKey(t *testing.T) {
	s := New()
	mustSet(t, s, "TZ=UTC", "--env", Flag)
	if err := s.Default("TZ=Europe/Helsinki", "host timezone", Builtin); err != nil {
		t.Fatalf("Default: %v", err)
	}

	want := []Pair{{"TZ", "UTC"}}
	if got := pairs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Pairs() = %#v, want %#v -- the flag must win and be emitted once", got, want)
	}
}

func TestDefaultSetsWhenAbsent(t *testing.T) {
	s := New()
	if err := s.Default("TZ=Europe/Helsinki", "host timezone", Builtin); err != nil {
		t.Fatalf("Default: %v", err)
	}
	want := []Pair{{"TZ", "Europe/Helsinki"}}
	if got := pairs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Pairs() = %#v, want %#v", got, want)
	}
}

// bash's env_default extracts the key and returns early *without validating*
// when the key is present, so a present key must not be re-validated.
func TestDefaultDoesNotValidateWhenKeyPresent(t *testing.T) {
	s := New()
	mustSet(t, s, "PATHISH=ok", "--env", Flag)
	if err := s.Default("PATHISH=whatever", "builtin", Builtin); err != nil {
		t.Fatalf("Default on a present key should be a no-op, got %v", err)
	}
}

// --- Summary ---------------------------------------------------------------

func TestSummary(t *testing.T) {
	flagOnly := New()
	mustSet(t, flagOnly, "FOO=1", "--env", Flag)
	mustSet(t, flagOnly, "BAR=2", "--env", Flag)

	fileOnly := New()
	mustSet(t, fileOnly, "BAZ=3", ".claude-contained/env:1", File)

	both := New()
	mustSet(t, both, "FOO=1", "--env", Flag)
	mustSet(t, both, "BAZ=3", ".claude-contained/env:1", File)

	builtinOnly := New()
	if err := builtinOnly.Default("TZ=UTC", "host timezone", Builtin); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		store *Store
		want  string
	}{
		{"flags only", flagOnly, "env: FOO, BAR (--env)"},
		{"file only", fileOnly, "env: BAZ (.claude-contained/env)"},
		{"both, semicolon separated", both, "env: FOO (--env); BAZ (.claude-contained/env)"},
		// Built-ins are never reported -- TZ is set on almost every run and would
		// otherwise print on every launch.
		{"built-ins are invisible", builtinOnly, ""},
		{"empty store", New(), ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.store.Summary(); got != tc.want {
				t.Errorf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- LoadFile --------------------------------------------------------------

func TestLoadFileBasics(t *testing.T) {
	content := "# a comment\n" +
		"  # an indented comment\n" +
		"\n" +
		"BAZ=\"from file\"\n" +
		"QUX=plain\n" +
		"SPACED=a b c\n"

	s := New()
	if err := s.LoadFile([]byte(content)); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	want := []Pair{{"BAZ", "from file"}, {"QUX", "plain"}, {"SPACED", "a b c"}}
	if got := pairs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Pairs() = %#v, want %#v", got, want)
	}
}

func TestLoadFileQuoteStripping(t *testing.T) {
	cases := []struct{ line, key, want string }{
		{`A="double"`, "A", "double"},
		{`B='single'`, "B", "single"},
		{`C="mismatched'`, "C", `"mismatched'`},
		{`D="`, "D", `"`},       // one character: too short to be a pair
		{`E=""`, "E", ""},       // exactly a pair, yields empty
		{`F=""x""`, "F", `"x"`}, // only the outermost pair comes off
		{`G=no quotes`, "G", "no quotes"},
		{`H="unterminated`, "H", `"unterminated`},
	}
	for _, tc := range cases {
		s := New()
		if err := s.LoadFile([]byte(tc.line + "\n")); err != nil {
			t.Fatalf("LoadFile(%q): %v", tc.line, err)
		}
		got := pairs(s)
		if len(got) != 1 || got[0].Key != tc.key || got[0].Value != tc.want {
			t.Errorf("LoadFile(%q) = %#v, want value %q", tc.line, got, tc.want)
		}
	}
}

func TestLoadFileWhitespaceAndLineEndings(t *testing.T) {
	// Leading whitespace is stripped; trailing whitespace is deliberately NOT,
	// so it stays part of the value.
	s := New()
	if err := s.LoadFile([]byte("   INDENTED=yes\nTRAILING=bar   \nCRLF=ok\r\n")); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	want := []Pair{{"INDENTED", "yes"}, {"TRAILING", "bar   "}, {"CRLF", "ok"}}
	if got := pairs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Pairs() = %#v, want %#v", got, want)
	}
}

func TestLoadFileFinalLineWithoutNewline(t *testing.T) {
	s := New()
	if err := s.LoadFile([]byte("NOEOL=ok")); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := pairs(s); len(got) != 1 || got[0] != (Pair{"NOEOL", "ok"}) {
		t.Errorf("Pairs() = %#v, want NOEOL=ok", got)
	}
}

func TestLoadFileEmpty(t *testing.T) {
	for _, content := range []string{"", "\n", "\n\n", "# only a comment\n"} {
		s := New()
		if err := s.LoadFile([]byte(content)); err != nil {
			t.Fatalf("LoadFile(%q): %v", content, err)
		}
		if got := pairs(s); len(got) != 0 {
			t.Errorf("LoadFile(%q) produced %#v, want nothing", content, got)
		}
	}
}

func TestLoadFileSplitsOnFirstEquals(t *testing.T) {
	s := New()
	if err := s.LoadFile([]byte("CONN=k=v;x=y\n")); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got := pairs(s); len(got) != 1 || got[0].Value != "k=v;x=y" {
		t.Errorf("Pairs() = %#v, want CONN=k=v;x=y", got)
	}
}

// The file is agent-writable, so it is parsed literally. A command substitution
// must survive as text and never be evaluated.
func TestLoadFileIsNeverEvaluated(t *testing.T) {
	s := New()
	content := "EVIL=$(touch CANARY)\nALSO=`touch CANARY`\n"
	if err := s.LoadFile([]byte(content)); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	want := []Pair{{"EVIL", "$(touch CANARY)"}, {"ALSO", "`touch CANARY`"}}
	if got := pairs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Pairs() = %#v, want the text verbatim %#v", got, want)
	}
}

// Within the file the last line for a key wins, and the key keeps the position
// of its first appearance.
func TestLoadFileLastLineWinsKeepingPosition(t *testing.T) {
	s := New()
	if err := s.LoadFile([]byte("A=1\nB=2\nA=3\n")); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	want := []Pair{{"A", "3"}, {"B", "2"}}
	if got := pairs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Pairs() = %#v, want %#v", got, want)
	}
}

// A flag wins over the file, and the file line is skipped *entirely* -- so its
// validation never runs. That is what makes `-e LD_PRELOAD=...` plus a
// LD_PRELOAD line in the file succeed rather than fail.
func TestLoadFileSkipsKeysAlreadySetByFlagWithoutValidating(t *testing.T) {
	s := New()
	mustSet(t, s, "LD_PRELOAD=/tmp/lib.so", "--env", Flag)
	mustSet(t, s, "FOO=from-flag", "--env", Flag)

	if err := s.LoadFile([]byte("LD_PRELOAD=/tmp/evil.so\nFOO=from-file\nBAR=only-file\n")); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	want := []Pair{{"LD_PRELOAD", "/tmp/lib.so"}, {"FOO", "from-flag"}, {"BAR", "only-file"}}
	if got := pairs(s); !reflect.DeepEqual(got, want) {
		t.Errorf("Pairs() = %#v, want %#v", got, want)
	}
	// The skipped keys keep origin Flag, so the summary attributes them correctly.
	if got, want := s.Summary(), "env: LD_PRELOAD, FOO (--env); BAR (.claude-contained/env)"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

// A key set by the *file* is not protected the way a flag-set key is: a later
// file line still overwrites it.
func TestLoadFileDoesNotSkipKeysSetByTheFile(t *testing.T) {
	s := New()
	if err := s.LoadFile([]byte("A=first\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadFile([]byte("A=second\n")); err != nil {
		t.Fatal(err)
	}
	if got := pairs(s); len(got) != 1 || got[0].Value != "second" {
		t.Errorf("Pairs() = %#v, want A=second", got)
	}
}

func TestLoadFileErrorsNameTheLine(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "malformed line",
			content: "FOO=ok\nJUSTAKEY\n",
			want:    "error: .claude-contained/env:2: expected KEY=VALUE, got 'JUSTAKEY'",
		},
		{
			name:    "always-reserved key",
			content: "STAY_ROOT=1\n",
			want:    "error: .claude-contained/env:1: STAY_ROOT is reserved by claude-contained and cannot be set",
		},
		{
			name:    "file-only reserved key",
			content: "# comment\n\nNODE_OPTIONS=--require=/tmp/x.js\n",
			want: "error: .claude-contained/env:3: NODE_OPTIONS cannot be set from a project env file\n" +
				"       It is read by the sandbox wrapper itself; pass -e NODE_OPTIONS=... if you mean it.",
		},
		{
			name:    "bad key name",
			content: "9BAD=x\n",
			want:    "error: .claude-contained/env:1: not a valid environment variable name: '9BAD'",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := New().LoadFile([]byte(tc.content))
			if err == nil {
				t.Fatal("LoadFile succeeded, want rejection")
			}
			if got := err.Error(); got != tc.want {
				t.Errorf("message =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// Line numbers count every physical line, including comments and blanks, so an
// error points at the line the user actually sees in their editor.
func TestLoadFileLineNumbersCountEveryLine(t *testing.T) {
	err := New().LoadFile([]byte("\n\n# c\n\nSTAY_ROOT=1\n"))
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "env:5:") {
		t.Errorf("message %q should name line 5", err.Error())
	}
}
