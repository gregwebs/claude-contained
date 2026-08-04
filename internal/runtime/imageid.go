package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// probeImageID is Runtime.DescribeImage's shared identity probe: run `<bin> image inspect
// [format args] <ref>`, and classify the result into present, absent, or
// fault.
//
// The classification is the whole substance of this file, because "absent" is
// not an observation the caller merely records -- it is a claim the caller acts
// on destructively. Absence of the *base* image prints "run --rebuild=full
// first", which is unhelpable advice on a machine where the image is right
// there; absence of a *derived* image triggers a confirmation prompt and a
// multi-minute build. So absence is only ever reported once the probe
// subcommand is known to exist:
//
//	exit 0, parse yields an id       -> present. The ordinary case.
//	exit 0, parse yields nothing     -> fault. The probe answered and we could
//	                                   not read it, which means our reading is
//	                                   wrong, not that the image is missing.
//	                                   Reporting absence here is what produces
//	                                   an endless silent rebuild loop.
//	nonzero exit, `--help` exits 0   -> absent. The subcommand exists, so the
//	                                   failure was about the reference.
//	nonzero exit, `--help` fails     -> fault, naming the binary and the argv.
//
// The capability probe is `<bin> image inspect --help` rather than matching the
// runtime's error text: "No such image" is locale-, version- and
// wording-dependent and both runtimes are free to change it, while --help asks
// the only question that matters -- does this CLI have this subcommand -- with
// a plain exit status, no daemon round trip (help is client-side on both) and
// nothing to parse. It runs only on the failure path, so the common case costs
// nothing and the worst case in one run is two extra --help invocations.
//
// Rejected: trying `image inspect` and falling back to `images inspect`.
// Guessing twice is still guessing, doubles the surface, and turns a
// diagnosable error into a silent second guess.
//
// Rejected: `image ls --quiet --no-trunc <ref>`, where exit 0 plus empty output
// is an unambiguous absence needing no discrimination at all. Attractive for
// Docker, but Apple's listing subcommand carries exactly the same spelling
// uncertainty as its inspect subcommand, so it buys nothing where the risk
// actually is and would make the two implementations diverge in shape.
//
// parse turns the successful probe's stdout into an id, or "" when it finds
// none. It is never handed a failed probe's output.
func probeImageID(
	ctx context.Context, bin, ref string, formatArgs []string, parse func(raw []byte) string,
) (string, bool, error) {
	args := append([]string{"image", "inspect"}, formatArgs...)
	args = append(args, ref)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if runErr == nil {
		if id := parse(stdout.Bytes()); id != "" {
			return id, true, nil
		}
		return "", false, fmt.Errorf(
			"%s reported an image for %s that could not be read: `%s` succeeded but carried no identifier",
			bin, ref, strings.Join(append([]string{bin}, args...), " "))
	}

	if supportsImageInspect(ctx, bin) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("%s does not understand `image inspect`: %s", bin, firstLine(stderr.String()))
}

// supportsImageInspect reports whether the CLI has the subcommand at all. Both
// output streams are discarded: only the exit status is being asked about, and
// a help text on stderr is not a diagnosis anyone wants relayed.
func supportsImageInspect(ctx context.Context, bin string) bool {
	return exec.CommandContext(ctx, bin, "image", "inspect", "--help").Run() == nil
}

// firstLine keeps a diagnosis to one line. A runtime that rejects a subcommand
// usually follows with its whole usage text, which would bury the error message
// the caller is about to print.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	if s == "" {
		return "no diagnostic output"
	}
	return s
}
