package imagescript

// Tests for image/tool-env.sh, the in-container tool-environment resolver. It
// prepends generic paths, then applies layer fragments (KEY=VALUE files) in
// which ONLY complete ${HOME}/$HOME and ${PATH}/$PATH references expand and
// nothing is ever shell-evaluated; it refuses reserved keys; an explicit process
// value (and a launcher marker) win over a fragment; and it finally execs the
// requested command with the resolved environment. The fragments are checked-in
// shell-syntax fixtures under testdata/ because their interpretation is the
// contract. Each test execs `env` and parses the dump.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const toolEnvBasePath = "/bin:/usr/bin"

func testdataPath(t *testing.T, rel ...string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(append([]string{wd, "testdata"}, rel...)...)
}

// resolveToolEnv runs tool-env with a fragment directory and returns the
// resolved environment the command it exec'd would see.
func resolveToolEnv(t *testing.T, home, fragDir string, extraEnv ...string) map[string]string {
	t.Helper()
	res := runScript(t, scriptOpts{
		Script: scriptPath(t, "tool-env.sh"),
		Args:   []string{"--directory", fragDir, "env"},
		Env:    extraEnv,
		Path:   toolEnvBasePath,
		Home:   home,
	})
	if res.Code != 0 {
		t.Fatalf("tool-env exit %d, want 0\nstderr:\n%s", res.Code, res.Stderr)
	}
	return parseEnvOutput(res.Stdout)
}

func assertEnv(t *testing.T, env map[string]string, key, want string) {
	t.Helper()
	if got := env[key]; got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

func assertEnvAbsent(t *testing.T, env map[string]string, key string) {
	t.Helper()
	if got, ok := env[key]; ok {
		t.Errorf("%s is set to %q, want unset", key, got)
	}
}

func TestToolEnvBaseAddsOnlyGenericPathsNoJava(t *testing.T) {
	env := resolveToolEnv(t, "/host/home", filepath.Join(t.TempDir(), "missing"))
	assertEnv(t, env, "PATH", "/host/home/.local/bin:/opt/claude:/bin:/usr/bin")
	for _, k := range []string{"JAVA_HOME", "JAVA_TOOL_OPTIONS", "MAVEN_OPTS"} {
		assertEnvAbsent(t, env, k)
	}
}

func TestToolEnvFragmentsExpandOnlyHomeAndPathNeverEvaluated(t *testing.T) {
	// The no-eval sentinel is a per-test path under t.TempDir() rather than a
	// shared global, so a pre-existing or foreign file can never turn a correct
	// run into a false failure. The fragment carries `$(touch <sentinel>)`; if
	// tool-env ever evaluated it, the sentinel would appear.
	sentinel := filepath.Join(t.TempDir(), "must-not-run")
	fragDir := t.TempDir()
	writeFragment(t, fragDir, "10-tool", "CACHE=${HOME}/.cache/tool\nPATH=${HOME}/bin:$PATH\nLITERAL=$(touch "+sentinel+")\n")
	writeFragment(t, fragDir, "20-path", "PATH=/opt/tool/bin:$PATH\n")

	env := resolveToolEnv(t, "/host/home", fragDir)

	assertEnv(t, env, "CACHE", "/host/home/.cache/tool")
	if !strings.Contains(env["PATH"], "/opt/tool/bin:/host/home/bin:/host/home/.local/bin:/opt/claude:") {
		t.Errorf("PATH = %q, want the fragment-prepended entries with $PATH expanded", env["PATH"])
	}
	assertEnv(t, env, "LITERAL", "$(touch "+sentinel+")")
	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("tool-env evaluated a $(...) fragment value: the sentinel %s was created", sentinel)
	}
}

func writeFragment(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestToolEnvReservedKeysRefused(t *testing.T) {
	reserved := []string{
		"STAY_ROOT", "SRT_SETTINGS_PATH", "HOST_UID", "HOME", "LD_PRELOAD",
		"LD_AUDIT", "BASH_ENV", "ENV", "NODE_OPTIONS", "CLAUDE_CONTAINED_ZELLIJ",
	}
	for _, key := range reserved {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			writeFragment(t, dir, "10-tool", key+"=bad\n")
			res := runScript(t, scriptOpts{
				Script: scriptPath(t, "tool-env.sh"),
				Args:   []string{"--directory", dir, "true"},
				Path:   toolEnvBasePath,
				Home:   "/host/home",
			})
			if res.Code == 0 {
				t.Errorf("exit 0, want nonzero for reserved key %s", key)
			}
			if !strings.Contains(res.Stderr, key+" is reserved") {
				t.Errorf("stderr %q is missing %q", res.Stderr, key+" is reserved")
			}
		})
	}
}

func TestToolEnvFragmentSuppliesCompleteJavaEnv(t *testing.T) {
	env := resolveToolEnv(t, "/host/home", testdataPath(t, "tool-env", "java"))
	assertEnv(t, env, "JAVA_HOME", "/opt/custom-java")
	assertEnv(t, env, "JAVA_TOOL_OPTIONS", "-XX:+UseG1GC -XX:+AllowEnhancedClassRedefinition -Dvaadin.productionMode=false")
	assertEnv(t, env, "MAVEN_OPTS", "-Dmaven.repo.local=/host/home/.claude-contained/cache/maven")
	assertEnv(t, env, "PATH", "/opt/custom-java/bin:/opt/maven/bin:/opt/jbang/bin:/host/home/.local/bin:/opt/claude:/bin:/usr/bin")
}

func TestToolEnvExplicitJavaHomeWinsOverFragment(t *testing.T) {
	env := resolveToolEnv(t, "/host/home", testdataPath(t, "tool-env", "java"), "JAVA_HOME=/explicit")
	assertEnv(t, env, "JAVA_HOME", "/explicit")
}

func TestToolEnvDirectImagePreservesImageJavaDefaults(t *testing.T) {
	env := resolveToolEnv(t, "/home/dev", testdataPath(t, "tool-env", "java"),
		"JAVA_HOME=/opt/jbr",
		"JAVA_TOOL_OPTIONS=from-image",
		"MAVEN_OPTS=-Dmaven.repo.local=/home/dev/.claude-contained/cache/maven",
	)
	assertEnv(t, env, "JAVA_HOME", "/opt/jbr")
	assertEnv(t, env, "JAVA_TOOL_OPTIONS", "from-image")
	assertEnv(t, env, "MAVEN_OPTS", "-Dmaven.repo.local=/home/dev/.claude-contained/cache/maven")
}

func TestToolEnvExplicitEnvKeysProtectKeyFromFragmentOverride(t *testing.T) {
	env := resolveToolEnv(t, "/host/home", testdataPath(t, "tool-env", "java"),
		"CLAUDE_CONTAINED_EXPLICIT_ENV_KEYS=JAVA_HOME",
		"JAVA_HOME=/explicit",
		"JAVA_TOOL_OPTIONS=from-image",
		"MAVEN_OPTS=-Dmaven.repo.local=/home/dev/.claude-contained/cache/maven",
	)
	assertEnv(t, env, "JAVA_HOME", "/explicit")
	assertEnv(t, env, "JAVA_TOOL_OPTIONS", "-XX:+UseG1GC -XX:+AllowEnhancedClassRedefinition -Dvaadin.productionMode=false")
	assertEnv(t, env, "MAVEN_OPTS", "-Dmaven.repo.local=/host/home/.claude-contained/cache/maven")
	assertEnvAbsent(t, env, "CLAUDE_CONTAINED_EXPLICIT_ENV_KEYS")
}

func TestToolEnvExplicitProcessValueWinsOverFragment(t *testing.T) {
	env := resolveToolEnv(t, "/host/home", testdataPath(t, "tool-env", "choice"), "CHOICE=from-user")
	assertEnv(t, env, "CHOICE", "from-user")
}

func TestToolEnvOnlyCompleteHomePathRefsExpand(t *testing.T) {
	env := resolveToolEnv(t, "/host/home", testdataPath(t, "tool-env", "partial"))
	assertEnv(t, env, "PREFIX", "$HOMELESS:$PATHOLOGY")
	assertEnv(t, env, "SENTINEL", "__CLAUDE_CONTAINED_HOME_REF__")
}
