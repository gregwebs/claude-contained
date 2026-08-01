package runtime

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestDiagnosticArgvRedactsEveryEnvironmentOperand(t *testing.T) {
	const secret = "DO-NOT-LOG"
	argv := []string{
		"docker", "run", "--name", "visible",
		"-e", "TOKEN=" + secret,
		"-e", "MALFORMED-" + secret,
		"-e", "-e", "NESTED=" + secret,
		"-e",
	}
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, nil))
	logger.Info("runtime argv", "argv", DiagnosticArgv(argv))
	got := out.String()
	if strings.Contains(got, secret) {
		t.Fatalf("diagnostic argv leaked environment value: %q", got)
	}
	for _, visible := range []string{"docker", "--name", "visible", "TOKEN=<redacted>"} {
		if !strings.Contains(got, visible) {
			t.Errorf("diagnostic argv missing %q: %q", visible, got)
		}
	}
}
