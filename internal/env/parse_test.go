package env

import (
	"reflect"
	"testing"
)

// splitLines has to agree with bash's `while IFS= read -r line || [[ -n "$line" ]]`
// on every shape of input, because the line number it produces ends up in user
// -facing rejection messages.
func TestSplitLines(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"empty", "", nil},
		{"one line with newline", "a\n", []string{"a"}},
		{"one line without newline", "a", []string{"a"}},
		{"a lone newline is one empty line", "\n", []string{""}},
		{"two newlines are two empty lines", "\n\n", []string{"", ""}},
		{"trailing newline adds no phantom line", "a\nb\n", []string{"a", "b"}},
		{"missing trailing newline still yields both", "a\nb", []string{"a", "b"}},
		{"interior blank line is kept for numbering", "a\n\nb\n", []string{"a", "", "b"}},
		{"lone CRLF", "\r\n", []string{"\r"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitLines(tc.content); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitLines(%q) = %#v, want %#v", tc.content, got, tc.want)
			}
		})
	}
}

func TestStripOnePair(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"double"`, "double"},
		{`'single'`, "single"},
		{`"mismatched'`, `"mismatched'`},
		{`'mismatched"`, `'mismatched"`},
		{`""`, ""},
		{`''`, ""},
		{`"`, `"`}, // one character cannot be a pair
		{`'`, `'`},
		{``, ``},
		{`""x""`, `"x"`}, // only the outermost pair comes off
		{`"unterminated`, `"unterminated`},
		{`unquoted`, `unquoted`},
		{`a"b"c`, `a"b"c`}, // quotes must surround, not merely appear
	}
	for _, tc := range cases {
		if got := stripOnePair(tc.in); got != tc.want {
			t.Errorf("stripOnePair(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
