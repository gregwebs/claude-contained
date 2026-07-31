package host

import "testing"

// The expected values here were produced by running the real bash function
// (`CLAUDE_CONTAINED_LIB_ONLY=1 source ./claude-contained; sanitize_foldername X`),
// not derived from reading it. That matters: the point of the table is to pin
// the behavior a tidier reimplementation would quietly change.
func TestSanitizeFolderName(t *testing.T) {
	cases := []struct {
		in   string
		want string
		why  string
	}{
		{"My-App", "my-app", "baseline"},
		{"abcdefghijklmnopqrs-tuv", "abcdefghijklmnopqrs-", "trailing dash survives truncation"},
		{"aaaaaaaaaaaaaaaaaaaa-bbb", "aaaaaaaaaaaaaaaaaaaa", "truncation without a trailing dash"},
		{"My Café", "my-caf", "non-ASCII, trailing multibyte"},
		{"Ünïcödé", "n-c-d", "non-ASCII throughout"},
		{"résumé-app", "r-sum-app", "non-ASCII interior"},
		{"日本語プロジェクト", "root", "all-multibyte collapses to empty, then the fallback"},
		{"a.b_c", "a-b-c", "'.' and '_' are not alphanumeric"},
		{"/", "root", "basename of / sanitizes to empty"},
		{"a//b/", "b", "trailing slashes are stripped before the basename"},
		// basename(1) treats a leading dash as an option and fails, and bash's
		// pipeline swallows that into the empty-name fallback. We match the
		// result but deliberately not the BSD-specific usage text it prints.
		{"-foo", "root", "leading dash defeats basename"},
	}

	for _, tc := range cases {
		if got := SanitizeFolderName(tc.in); got != tc.want {
			t.Errorf("SanitizeFolderName(%q) = %q, want %q (%s)", tc.in, got, tc.want, tc.why)
		}
	}
}

// bash's `tr '[:upper:]' '[:lower:]'` is multibyte-aware under the UTF-8
// locales that users and the test suites actually run in: it maps 'İ' to a
// plain 'i', so the name keeps a leading letter instead of losing it to the
// non-alphanumeric substitution. Verified against the bash function.
//
// Under LC_ALL=C the same pipeline yields "stanbul" instead, so this is the one
// place the port is pinned to a locale rather than to bash unconditionally.
func TestSanitizeFolderNameLowercasingIsUnicodeAware(t *testing.T) {
	if got := SanitizeFolderName("İstanbul"); got != "istanbul" {
		t.Errorf("SanitizeFolderName(%q) = %q, want %q", "İstanbul", got, "istanbul")
	}
}
