package imagescript

// Tests for image/host-forward.sh. The property that matters is embarrassingly
// small and was silently broken in production for months: socat takes TWO
// address arguments, and a quoting change once collapsed them into one, so socat
// exited "exactly 2 addresses required" and forwarded nothing. Every assertion
// here inspects socat's argument vector, so a future collapse (which a shell
// linter will keep suggesting) fails loudly. The relays are backgrounded, so the
// script exits before the stub records; WaitForEvents is the readiness wait.

import (
	"sort"
	"testing"
	"time"

	"claude-contained/internal/blackbox"
)

func runHostForward(t *testing.T, ports string) *blackbox.Stubs {
	t.Helper()
	stubs := blackbox.NewStubs(t, "socat")
	env := []string(nil)
	if ports != "\x00unset" {
		env = []string{"HOST_FORWARD_PORTS=" + ports}
	}
	res := runScript(t, scriptOpts{
		Script: scriptPath(t, "host-forward.sh"),
		Env:    env,
		Stubs:  stubs,
	})
	if res.Code != 0 {
		t.Fatalf("host-forward.sh exit %d, want 0\nstderr:\n%s", res.Code, res.Stderr)
	}
	return stubs
}

// relayArgs returns each socat invocation's address arguments, verifying every
// relay received exactly two -- a relay recorded with one argument is the
// collapse regression, and an empty set never passes an address assertion.
func relayArgs(t *testing.T, stubs *blackbox.Stubs) [][]string {
	t.Helper()
	var out [][]string
	for _, e := range stubs.Events(t) {
		if e.Bin != "socat" {
			continue
		}
		if len(e.Argv) != 2 {
			t.Fatalf("socat received %d arguments, want exactly 2 (address collapse): %v", len(e.Argv), e.Argv)
		}
		out = append(out, e.Argv)
	}
	return out
}

func relaySet(argv [][]string) map[string]bool {
	set := map[string]bool{}
	for _, a := range argv {
		set[a[0]+" "+a[1]] = true
	}
	return set
}

func TestHostForwardOneMappingStartsOneRelayWithTwoAddresses(t *testing.T) {
	stubs := runHostForward(t, "3845")
	if !blackbox.WaitForEvents(t, stubs, 1, 10*time.Second) {
		t.Fatal("the socat relay never started")
	}
	if got := relayArgs(t, stubs); len(got) != 1 {
		t.Fatalf("started %d relays, want 1", len(got))
	}
}

func TestHostForwardBarePortMapsSameToSame(t *testing.T) {
	stubs := runHostForward(t, "3845")
	if !blackbox.WaitForEvents(t, stubs, 1, 10*time.Second) {
		t.Fatal("the socat relay never started")
	}
	set := relaySet(relayArgs(t, stubs))
	if !set["TCP-LISTEN:3845,fork,reuseaddr TCP:host.local:3845"] {
		t.Errorf("a bare port did not map to the same host port; relays: %v", set)
	}
}

func TestHostForwardLocalHostSplitsPorts(t *testing.T) {
	stubs := runHostForward(t, "3845:9000")
	if !blackbox.WaitForEvents(t, stubs, 1, 10*time.Second) {
		t.Fatal("the socat relay never started")
	}
	set := relaySet(relayArgs(t, stubs))
	if !set["TCP-LISTEN:3845,fork,reuseaddr TCP:host.local:9000"] {
		t.Errorf("LOCAL:HOST did not map the two ports independently; relays: %v", set)
	}
}

func TestHostForwardCommaListOneRelayPerMapping(t *testing.T) {
	stubs := runHostForward(t, "3845,8080:9090")
	if !blackbox.WaitForEvents(t, stubs, 2, 10*time.Second) {
		t.Fatal("both socat relays never started")
	}
	got := relayArgs(t, stubs)
	if len(got) != 2 {
		t.Fatalf("started %d relays, want 2", len(got))
	}
	set := relaySet(got)
	for _, want := range []string{
		"TCP-LISTEN:3845,fork,reuseaddr TCP:host.local:3845",
		"TCP-LISTEN:8080,fork,reuseaddr TCP:host.local:9090",
	} {
		if !set[want] {
			t.Errorf("comma list is missing relay %q; relays: %v", want, set)
		}
	}
}

func TestHostForwardListenerKeepsForkReuseaddr(t *testing.T) {
	stubs := runHostForward(t, "3845")
	if !blackbox.WaitForEvents(t, stubs, 1, 10*time.Second) {
		t.Fatal("the socat relay never started")
	}
	listener := relayArgs(t, stubs)[0][0]
	if listener != "TCP-LISTEN:3845,fork,reuseaddr" {
		t.Errorf("listener = %q, want it to keep fork,reuseaddr", listener)
	}
}

func TestHostForwardUnsetStartsNoRelays(t *testing.T) {
	assertNoRelays(t, runHostForward(t, "\x00unset"))
}

func TestHostForwardEmptyStartsNoRelays(t *testing.T) {
	assertNoRelays(t, runHostForward(t, ""))
}

// assertNoRelays proves the negative: no HOST_FORWARD_PORTS starts nothing.
// The script forks no background process at all in this case, so a bounded
// settle window confirms none appears rather than merely that none has yet.
func assertNoRelays(t *testing.T, stubs *blackbox.Stubs) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if events := stubs.Events(t); len(events) != 0 {
		var argvs [][]string
		for _, e := range events {
			argvs = append(argvs, e.Argv)
		}
		sort.Slice(argvs, func(i, j int) bool { return len(argvs[i]) < len(argvs[j]) })
		t.Errorf("started %d relay(s) with no ports requested: %v", len(events), argvs)
	}
}
