package imagescript

// Tests for image/srt-settings.sh, which generates the per-run srt sandbox
// policy with jq. Two properties matter most and regress easily: every writable
// mount must appear in filesystem.allowWrite (srt denies writes by default and,
// on Linux, matches paths literally, so a missing entry silently breaks a tool),
// and the generated file must not be writable by the sandboxed user (an
// allowlist the agent can rewrite is not a control). The assertions parse the
// generated JSON structurally, mirroring the retired suite's jq queries.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// genSRT runs srt-settings.sh for a home directory and returns the result plus
// the path it was told to write. HOST_HOME (not the ambient HOME) drives the
// derived tool-config paths, and SSH_AUTH_SOCK is deliberately never in the
// scratch environment unless a case sets it.
func genSRT(t *testing.T, home string, env ...string) (scriptResult, string) {
	t.Helper()
	requireJQ(t)
	out := filepath.Join(t.TempDir(), "settings.json")
	full := append([]string{"HOST_HOME=" + home, "SRT_SETTINGS_PATH=" + out}, env...)
	res := runScript(t, scriptOpts{
		Script: scriptPath(t, "srt-settings.sh"),
		Env:    full,
		Home:   home,
	})
	return res, out
}

// homeWithProfile returns a scratch HOME whose .claude-contained dir exists, the
// place the user policy file lives.
func homeWithProfile(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude-contained"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

func writeUserPolicy(t *testing.T, home, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, ".claude-contained", "srt-settings.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodePolicy(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated policy: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("generated policy is not valid JSON: %v\n%s", err, data)
	}
	return m
}

func objAt(t *testing.T, m map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := m
	for _, k := range path {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("policy path %v: %q is not an object", path, k)
		}
		cur = next
	}
	return cur
}

// arrAt returns the JSON array at the given object path, failing if the key is
// absent or not an array -- so "required array present even when empty" is a
// real assertion, not a nil check.
func arrAt(t *testing.T, m map[string]any, path ...string) []any {
	t.Helper()
	parent := objAt(t, m, path[:len(path)-1]...)
	v, ok := parent[path[len(path)-1]]
	if !ok {
		t.Fatalf("policy is missing %v", path)
	}
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("policy %v is %T, want an array", path, v)
	}
	return arr
}

func arrHas(arr []any, want string) bool {
	for _, v := range arr {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

func assertWritable(t *testing.T, m map[string]any, paths ...string) {
	t.Helper()
	writes := arrAt(t, m, "filesystem", "allowWrite")
	for _, p := range paths {
		if !arrHas(writes, p) {
			t.Errorf("filesystem.allowWrite is missing %q", p)
		}
	}
}

func assertNotWritable(t *testing.T, m map[string]any, paths ...string) {
	t.Helper()
	writes := arrAt(t, m, "filesystem", "allowWrite")
	for _, p := range paths {
		if arrHas(writes, p) {
			t.Errorf("filesystem.allowWrite unexpectedly contains %q", p)
		}
	}
}

func TestSrtSettingsGitProtectDirsWritable(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/proj/alpha:/proj/beta")
	assertWritable(t, decodePolicy(t, out), "/proj/alpha", "/proj/beta")
}

func TestSrtSettingsToolConfigDirsAndTmpWritable(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	assertWritable(t, decodePolicy(t, out),
		home+"/.claude",
		home+"/.claude-contained/claude",
		home+"/.codex",
		home+"/.claude-contained",
		"/tmp",
	)
}

func TestSrtSettingsSharedStateExcludesM2AndVaadin(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	m := decodePolicy(t, out)
	assertWritable(t, m, home+"/.claude-contained")
	assertNotWritable(t, m, home+"/.m2", home+"/.vaadin")
}

func TestSrtSettingsEnableWeakerNestedSandbox(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	if m := decodePolicy(t, out); m["enableWeakerNestedSandbox"] != true {
		t.Errorf("enableWeakerNestedSandbox = %v, want true", m["enableWeakerNestedSandbox"])
	}
}

func TestSrtSettingsAllowLocalBinding(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	if got := objAt(t, decodePolicy(t, out), "network")["allowLocalBinding"]; got != true {
		t.Errorf("network.allowLocalBinding = %v, want true", got)
	}
}

func TestSrtSettingsDefaultDomainsIncludeAnthropicAPI(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	if !arrHas(arrAt(t, decodePolicy(t, out), "network", "allowedDomains"), "api.anthropic.com") {
		t.Error("default allowedDomains is missing api.anthropic.com")
	}
}

func TestSrtSettingsDefaultDomainsIncludeOAuthHosts(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	domains := arrAt(t, decodePolicy(t, out), "network", "allowedDomains")
	for _, host := range []string{"platform.claude.com", "claude.ai"} {
		if !arrHas(domains, host) {
			t.Errorf("default allowedDomains is missing the OAuth host %q", host)
		}
	}
}

func TestSrtSettingsRequiredDenyArraysPresent(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	m := decodePolicy(t, out)
	_ = arrAt(t, m, "network", "deniedDomains")
	_ = arrAt(t, m, "filesystem", "denyRead")
	_ = arrAt(t, m, "filesystem", "denyWrite")
}

func TestSrtSettingsSrtAllowHostsUnioned(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p", "SRT_ALLOW_HOSTS=one.example,two.example")
	domains := arrAt(t, decodePolicy(t, out), "network", "allowedDomains")
	for _, host := range []string{"one.example", "two.example"} {
		if !arrHas(domains, host) {
			t.Errorf("SRT_ALLOW_HOSTS host %q was not added", host)
		}
	}
}

const userPolicyFixture = `{
  "network": {
    "allowedDomains": ["corp.example"],
    "deniedDomains": ["blocked.example"],
    "tlsTerminate": { "excludeDomains": ["pinned.example"] }
  },
  "filesystem": {
    "denyRead": ["/secret/read"],
    "denyWrite": ["/secret/write"]
  },
  "ignoreViolations": { "some-rule": ["path"] }
}`

func TestSrtSettingsUserFileFlagAndDefaultsMerge(t *testing.T) {
	home := homeWithProfile(t)
	writeUserPolicy(t, home, userPolicyFixture)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p", "SRT_ALLOW_HOSTS=flag.example")
	domains := arrAt(t, decodePolicy(t, out), "network", "allowedDomains")
	for _, host := range []string{"corp.example", "flag.example", "api.anthropic.com"} {
		if !arrHas(domains, host) {
			t.Errorf("merged allowedDomains is missing %q", host)
		}
	}
}

func TestSrtSettingsUserDenyListsCarriedThrough(t *testing.T) {
	home := homeWithProfile(t)
	writeUserPolicy(t, home, userPolicyFixture)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	m := decodePolicy(t, out)
	if !arrHas(arrAt(t, m, "network", "deniedDomains"), "blocked.example") {
		t.Error("user deniedDomains entry was dropped")
	}
	if !arrHas(arrAt(t, m, "filesystem", "denyRead"), "/secret/read") {
		t.Error("user denyRead entry was dropped")
	}
	if !arrHas(arrAt(t, m, "filesystem", "denyWrite"), "/secret/write") {
		t.Error("user denyWrite entry was dropped")
	}
}

func TestSrtSettingsUnknownUserKeysCarriedThrough(t *testing.T) {
	home := homeWithProfile(t)
	writeUserPolicy(t, home, userPolicyFixture)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	m := decodePolicy(t, out)
	if !arrHas(arrAt(t, m, "network", "tlsTerminate", "excludeDomains"), "pinned.example") {
		t.Error("unknown key network.tlsTerminate.excludeDomains was dropped")
	}
	iv, ok := m["ignoreViolations"].(map[string]any)
	if !ok {
		t.Fatal("unknown key ignoreViolations was dropped")
	}
	rule, ok := iv["some-rule"].([]any)
	if !ok || !arrHas(rule, "path") {
		t.Errorf("ignoreViolations[some-rule] = %v, want it to carry \"path\"", iv["some-rule"])
	}
}

func TestSrtSettingsMalformedUserFileFailsClosed(t *testing.T) {
	home := homeWithProfile(t)
	writeUserPolicy(t, home, "this is not json {{{")
	res, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	if res.Code == 0 {
		t.Error("a malformed user policy did not fail the generator (exit 0)")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("the generator wrote a policy despite the malformed user file: it did not fail closed")
	}
}

func TestSrtSettingsSshAgentSocketAllowedWhenSet(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p", "SSH_AUTH_SOCK=/ssh-agent")
	if !arrHas(arrAt(t, decodePolicy(t, out), "network", "allowUnixSockets"), "/ssh-agent") {
		t.Error("SSH_AUTH_SOCK socket was not allowed")
	}
}

func TestSrtSettingsNoUnixSocketsWithoutSshAuthSock(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	if got := arrAt(t, decodePolicy(t, out), "network", "allowUnixSockets"); len(got) != 0 {
		t.Errorf("allowUnixSockets = %v, want empty without SSH_AUTH_SOCK", got)
	}
}

func zellijMarkedEnv() []string {
	return []string{
		"GIT_PROTECT_DIRS=/p",
		"HOST_UID=1234",
		"CLAUDE_CONTAINED_ZELLIJ=1",
		"CLAUDE_CONTAINED_ZELLIJ_SESSION=cc-test",
	}
}

func TestSrtSettingsZellijSessionSocketOnlyForMarkedRuns(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, zellijMarkedEnv()...)
	want := "/tmp/claude-contained-zellij-runtime/zellij/contract_version_1/cc-test"
	if !arrHas(arrAt(t, decodePolicy(t, out), "network", "allowUnixSockets"), want) {
		t.Errorf("marked Zellij run did not allow its session socket %q", want)
	}
}

func TestSrtSettingsZellijEnablesAllUnixSockets(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, zellijMarkedEnv()...)
	if got := objAt(t, decodePolicy(t, out), "network")["allowAllUnixSockets"]; got != true {
		t.Errorf("network.allowAllUnixSockets = %v, want true for a Zellij run", got)
	}
}

func TestSrtSettingsZellijLiteralPathsWritable(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, zellijMarkedEnv()...)
	assertWritable(t, decodePolicy(t, out),
		home+"/.claude-contained/zellij/data",
		home+"/.claude-contained/zellij/cache/org/Zellij-Contributors/Zellij",
		"/tmp/claude-contained-zellij-runtime",
		"/tmp/claude-contained-zellij-runtime/zellij/contract_version_1",
		"/tmp/claude-contained-zellij-runtime/zellij/contract_version_1/cc-test",
		"/tmp/claude-contained-zellij-runtime/layouts/cc-test.kdl",
		"/tmp/zellij-1234/zellij-log/zellij.log",
	)
}

func TestSrtSettingsGeneratedPolicyIsMode444(t *testing.T) {
	home := homeWithProfile(t)
	_, out := genSRT(t, home, "GIT_PROTECT_DIRS=/p")
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat generated policy: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o444 {
		t.Errorf("generated policy mode = %o, want 444 (the sandboxed process must not be able to rewrite its own policy)", perm)
	}
}
