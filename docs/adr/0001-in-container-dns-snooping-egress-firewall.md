---
status: accepted
---

# Egress allowlist enforced by an in-container DNS-snooping firewall + SNI/Host proxy

The egress allowlist (`--allow-host`) must hold as a hard security boundary against
an adversarial `dev`-user process, not just a convenience. Apple's `container` CLI
(v1.1.0) has no host-level egress filtering, so enforcement has to live inside the
container itself, in the root-owned phase of `entrypoint.sh` before it drops to
`dev` — root is unfiltered by design (the enforcement plane), `dev` is filtered.

We considered three mechanisms: (1) **resolve allowed names once at container
start** and load their IPs into a static firewall — simplest, but breaks
mid-session as CDN-backed names rotate IPs; (2) a **TLS-terminating MITM proxy**
with an injected CA — closes the shared-CDN-IP bypass completely by validating the
certificate, but breaks certificate pinning and adds a trusted CA into the image;
(3) **dnsmasq feeding nftables sets live** (`--nftset`) plus a **passthrough
SNI/Host proxy** that dials out by the name it reads, not the client's original
destination.

We chose (3). dnsmasq only resolves allowlisted names (NXDOMAIN for everything
else) and pushes their IPs into an nftables set as they're looked up, so the
allowlist tracks IP rotation automatically. The residual risk of (3) alone — a
non-allowlisted name sharing a CDN IP with an allowlisted one — is closed by the
SNI/Host proxy: nftables NATs `dev`'s outbound 443/80 to it, it reads the SNI (443)
or Host header (80), and only then makes its own connection *to that name*, never
to the IP the client tried. No TLS interception is needed, so certificate
validation still happens end-to-end between the real client and server.

Static entries (`host.local`, literal IPs/CIDRs) bypass the proxy and connect
directly — they're for host services and known endpoints, not the CDN-sharing
problem. UDP 443 (QUIC) is blocked for `dev` so TLS traffic can't route around the
SNI check by skipping TCP entirely.
