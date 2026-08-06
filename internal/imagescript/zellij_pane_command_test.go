package imagescript

// Tests for image/zellij-pane-command.sh, which restores the tool environment
// before a generated Zellij pane command runs: previously-set XDG/TMPDIR/PATH/
// SHELL come back from the CLAUDE_CONTAINED_PRE_ZELLIJ_* stash, previously-unset
// ones are unset, all helper env is stripped, and when PATH was not stashed only
// the generic entries are prepended. The script execs `env`, which dumps the
// restored environment.

import (
	"strings"
	"testing"
)

func TestZellijPaneCommandRestoresEnvironment(t *testing.T) {
	home := t.TempDir()
	restored := "/opt/java/bin:/opt/maven/bin:" + home + "/.local/bin:/opt/claude:/usr/local/bin:/usr/bin:/bin"

	res := runScript(t, scriptOpts{
		Script: scriptPath(t, "zellij-pane-command.sh"),
		Args:   []string{"env"},
		Path:   "/usr/bin:/bin",
		Home:   home,
		Env: []string{
			"XDG_CACHE_HOME=/zellij/cache",
			"XDG_DATA_HOME=/zellij/data",
			"XDG_RUNTIME_DIR=/zellij/run",
			"TMPDIR=/tmp",
			"CLAUDE_CONTAINED_PRE_ZELLIJ_XDG_CACHE_HOME_SET=1",
			"CLAUDE_CONTAINED_PRE_ZELLIJ_XDG_CACHE_HOME=/before/cache",
			"CLAUDE_CONTAINED_PRE_ZELLIJ_XDG_DATA_HOME_SET=0",
			"CLAUDE_CONTAINED_PRE_ZELLIJ_XDG_RUNTIME_DIR_SET=1",
			"CLAUDE_CONTAINED_PRE_ZELLIJ_XDG_RUNTIME_DIR=/before/run",
			"CLAUDE_CONTAINED_PRE_ZELLIJ_TMPDIR_SET=1",
			"CLAUDE_CONTAINED_PRE_ZELLIJ_TMPDIR=/tmp/claude",
			"CLAUDE_CONTAINED_PRE_ZELLIJ_PATH_SET=1",
			"CLAUDE_CONTAINED_PRE_ZELLIJ_PATH=" + restored,
			"CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL_SET=1",
			"CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL=/bin/bash",
			"CLAUDE_CONTAINED_ZELLIJ_CONFIG=/etc/claude-contained/zellij/config.kdl",
			"CLAUDE_CONTAINED_ZELLIJ_SOCKET=/tmp/claude-contained-zellij-runtime/zellij/contract_version_1/test",
			"CLAUDE_CONTAINED_ZELLIJ_LAYOUT_DIR=/tmp/claude-contained-zellij-runtime/layouts",
			"CLAUDE_CONTAINED_ZELLIJ_TMP_DIR=/tmp/zellij-501",
		},
	})
	if res.Code != 0 {
		t.Fatalf("exit %d, want 0\nstderr:\n%s", res.Code, res.Stderr)
	}
	env := parseEnvOutput(res.Stdout)

	t.Run("restores_previously_set_xdg_and_tmpdir", func(t *testing.T) {
		assertEnv(t, env, "XDG_CACHE_HOME", "/before/cache")
		assertEnv(t, env, "XDG_RUNTIME_DIR", "/before/run")
		assertEnv(t, env, "TMPDIR", "/tmp/claude")
	})
	t.Run("restores_path_and_shell", func(t *testing.T) {
		assertEnv(t, env, "PATH", restored)
		assertEnv(t, env, "SHELL", "/bin/bash")
	})
	t.Run("unsets_previously_unset_xdg", func(t *testing.T) {
		assertEnvAbsent(t, env, "XDG_DATA_HOME")
	})
	t.Run("strips_pre_zellij_helper_env", func(t *testing.T) {
		for k := range env {
			if strings.HasPrefix(k, "CLAUDE_CONTAINED_PRE_ZELLIJ_") {
				t.Errorf("pre-Zellij helper var survived: %s", k)
			}
		}
	})
	t.Run("strips_zellij_only_helper_env", func(t *testing.T) {
		for _, k := range []string{
			"CLAUDE_CONTAINED_ZELLIJ_CONFIG",
			"CLAUDE_CONTAINED_ZELLIJ_SOCKET",
			"CLAUDE_CONTAINED_ZELLIJ_LAYOUT_DIR",
			"CLAUDE_CONTAINED_ZELLIJ_TMP_DIR",
		} {
			assertEnvAbsent(t, env, k)
		}
	})
}

func TestZellijPaneCommandAddsGenericPathsWhenUnstashed(t *testing.T) {
	home := t.TempDir()
	res := runScript(t, scriptOpts{
		Script: scriptPath(t, "zellij-pane-command.sh"),
		Args:   []string{"/usr/bin/env"},
		Path:   "/usr/bin:/bin",
		Home:   home,
	})
	if res.Code != 0 {
		t.Fatalf("exit %d, want 0\nstderr:\n%s", res.Code, res.Stderr)
	}
	env := parseEnvOutput(res.Stdout)
	assertEnv(t, env, "PATH", home+"/.local/bin:/opt/claude:/usr/local/bin:/usr/bin:/bin")
	if strings.Contains(env["PATH"], "/opt/jbr") || strings.Contains(env["PATH"], "/.sdkman/") {
		t.Errorf("PATH picked up toolchain paths it never stashed: %q", env["PATH"])
	}
}
