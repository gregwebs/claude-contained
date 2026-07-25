# Claude Code Contained

A bash-based containerization wrapper that runs AI coding assistants inside an Apple Containers or Docker sandbox with persistent state.

## Language

**Egress allowlist**:
The set of destinations the contained tool process (`dev`) may reach; every other destination is rejected. Opt-in via `--allow-host` / `CLAUDE_ALLOW_HOSTS`. A hard security boundary against an adversarial in-container process, not merely a guardrail against accidental egress.

**Allowlist entry**:
One item in an egress allowlist: either a DNS name (matches itself and all subdomains, reachable on every port) or a static destination (`host.local`, an IP, or a CIDR — reachable on every port unless given an optional `:PORT`).

**Enforcement plane**:
The root-owned processes inside the container that set up and operate the egress allowlist's firewall: entrypoint's firewall setup, dnsmasq, the SNI/Host proxy, and the existing `socat` host-forwards. Always unfiltered by the allowlist, and unreachable from the filtered plane (no `sudo` in the image).

**Filtered plane**:
The `dev` user's processes — the AI tool and anything it runs. All egress from this plane is subject to the egress allowlist once one is configured.
_Avoid_: "the container" (ambiguous — root inside the same container is the enforcement plane, not filtered)

**Base allowlist**:
The minimal set of hostnames a selected tool needs to function (its own API + auth endpoints, never telemetry/crash-reporting) — added automatically to the egress allowlist unless `--no-default-hosts` is given.
