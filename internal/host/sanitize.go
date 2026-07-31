package host

import "strings"

// maxFolderNameLen mirrors the `cut -c1-20` in sanitize_foldername.
const maxFolderNameLen = 20

// SanitizeFolderName is a byte-for-byte translation of sanitize_foldername
// (claude-contained:118-128). It names containers and Zellij sessions, so a
// tidier reimplementation would silently rename them.
//
// The detail that is easiest to get wrong: dash trimming happens *before*
// truncation, so truncating at 20 can legitimately leave a trailing dash
// ("abcdefghijklmnopqrs-tuv" -> "abcdefghijklmnopqrs-").
//
// On the lowercasing: bash uses `tr '[:upper:]' '[:lower:]'`, whose behavior is
// locale-dependent. Under LC_ALL=C it is byte-oriented and leaves a multibyte
// upper-case rune untouched; under the UTF-8 locales that users and the test
// suites actually run in, BSD tr is multibyte-aware and maps
// 'İ' to 'i' -- which is exactly what strings.ToLower does. Verified by
// experiment, so the Unicode-aware form is the faithful port here and the
// byte-oriented one would be the divergence. Every case in the golden table
// produces the same answer under both readings, because the dash substitution
// below flattens the difference for anything that stays non-ASCII.
//
// Bash also runs the argument through basename(1), which on a leading-dash
// input treats it as an option and fails with a usage message. We match the
// *result* of that (the empty-name fallback, "root") but deliberately not the
// message: it is BSD/GNU-specific text that would not survive a Linux host.
func SanitizeFolderName(path string) string {
	name := strings.ToLower(baseName(path))

	b := []byte(name)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			// keep
		default:
			// Every byte that is not ASCII-alphanumeric becomes a dash --
			// bytewise, so a multibyte rune contributes one dash per byte.
			// The collapse below makes that indistinguishable from one dash.
			b[i] = '-'
		}
	}

	var collapsed strings.Builder
	prevDash := false
	for _, c := range b {
		if c == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		collapsed.WriteByte(c)
	}

	name = collapsed.String()
	name = strings.TrimPrefix(name, "-")
	name = strings.TrimSuffix(name, "-")

	if len(name) > maxFolderNameLen {
		name = name[:maxFolderNameLen]
	}
	if name == "" {
		return "root"
	}
	return name
}

// baseName reproduces basename(1) rather than filepath.Base, which differs on
// the inputs that matter here: filepath.Base("") is ".", and basename "/" is
// "/" (which then sanitizes to empty, hence "root").
//
// A leading dash is where basename(1) bails out with a usage error; bash's
// pipeline swallows the failure and yields the empty-name fallback, so that is
// what we return.
func baseName(path string) string {
	if strings.HasPrefix(path, "-") {
		return ""
	}
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		// All slashes (or empty): basename prints "/" or ".", neither of which
		// survives sanitizing.
		return ""
	}
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}
