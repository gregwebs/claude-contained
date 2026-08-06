package imagescript

// Tests that the srt wrapper seams keep the sandbox-runtime invocation intact:
// an absolute settings path (so it resolves regardless of cwd) and a `--`
// separator (so the child's own flags are never parsed by srt). The contract is
// the exact wrapping text, not runtime behavior -- running entrypoint.sh needs
// root/chown/usermod -- so these are static-content assertions, matching the
// retired shell suite's grep.

import (
	"os"
	"strings"
	"testing"
)

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Errorf("%s does not contain the required line:\n%s", path, want)
	}
}

func TestSrtRunUsesAbsoluteSettingsPathAndChildFlagSeparator(t *testing.T) {
	assertFileContains(t, scriptPath(t, "srt-run.sh"),
		`exec /usr/local/bin/srt --settings "${SRT_SETTINGS_PATH:-/run/srt-settings.json}" -- "$@"`)
}

func TestEntrypointUsesAbsoluteSettingsPathAndChildFlagSeparator(t *testing.T) {
	assertFileContains(t, scriptPath(t, "entrypoint.sh"),
		`set -- /usr/local/bin/srt --settings "${SRT_SETTINGS_PATH:-/run/srt-settings.json}" -- "$@"`)
}
