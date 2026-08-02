package runtime

import "testing"

// Every not-found case must return "" without panicking: probeImageID turns ""
// into a *fault*, which is a diagnosis, while a panic is a crash and a wrong
// non-empty answer is a derived image named after nothing.
func TestParseAppleImageID(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			"array of one, nested lower-case descriptor",
			`[{"descriptor":{"digest":"sha256:aaaa"}}]`,
			"sha256:aaaa",
		},
		{
			"container CLI 1.1.0 shape: digest under configuration.descriptor",
			`[{"configuration":{"descriptor":{"digest":"sha256:1110"},"name":"x"},"id":"1110","variants":[]}]`,
			"sha256:1110",
		},
		{
			"capitalized spelling",
			`[{"Descriptor":{"Digest":"sha256:bbbb"}}]`,
			"sha256:bbbb",
		},
		{
			"single document, not an array",
			`{"descriptor":{"digest":"sha256:cccc"}}`,
			"sha256:cccc",
		},
		{
			"bare top-level digest",
			`{"digest":"sha256:dddd"}`,
			"sha256:dddd",
		},
		{
			"array of many: the first non-empty wins",
			`[{"descriptor":{}},{"descriptor":{"digest":"sha256:eeee"}}]`,
			"sha256:eeee",
		},
		{
			"an empty digest string falls through to the next spelling",
			`{"descriptor":{"digest":""},"Digest":"sha256:ffff"}`,
			"sha256:ffff",
		},
		{"empty array", `[]`, ""},
		{"null", `null`, ""},
		{"garbage", `not json at all`, ""},
		{"no digest anywhere", `[{"name":"claude-contained:latest"}]`, ""},
		{"digest is not a string", `{"digest":{"sha256":"x"}}`, ""},
		{"empty input", ``, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseAppleImageID([]byte(tc.raw)); got != tc.want {
				t.Errorf("parseAppleImageID(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
