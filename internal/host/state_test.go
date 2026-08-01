package host

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestStateLogValueIsAnAllowlist(t *testing.T) {
	const sentinel = "DO-NOT-LOG"
	state := State{
		Home: "/safe/home", UID: "501", GID: "20", Arch: sentinel + "-arch",
		Timezone: sentinel + "-timezone", Now: time.Unix(123, 0),
		GHToken: sentinel + "-token", Memory: sentinel + "-memory",
		DNSEnv: sentinel + "-dns", DNSEnvSet: true, ShareHostClaude: true,
		ContainerRuntime: sentinel + "-runtime", BuildContext: sentinel + "-build",
		LogLevel: sentinel + "-level",
	}
	var out bytes.Buffer
	slog.New(slog.NewTextHandler(&out, nil)).Info("host", "state", state)
	got := out.String()
	if strings.Contains(got, sentinel) {
		t.Fatalf("State.LogValue leaked an omitted raw field: %q", got)
	}
	for _, anchor := range []string{"state.uid=501", "state.arch=unknown", "state.gh_token_present=true", "state.log_level_env_set=true"} {
		if !strings.Contains(got, anchor) {
			t.Errorf("State.LogValue missing %q: %q", anchor, got)
		}
	}
}
