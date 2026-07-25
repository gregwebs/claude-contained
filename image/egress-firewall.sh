#!/bin/bash
# Sets up the opt-in egress allowlist firewall: a default-deny nftables
# ruleset for the `dev` user, fed by a DNS-snooping dnsmasq (so only the
# resolved IPs of allowlisted names become reachable) plus a root-owned
# SNI/Host passthrough proxy on 443/80 (so a shared CDN IP can't be used to
# reach a non-allowlisted name under the same address).
#
# Invoked by entrypoint.sh, as root, before the privilege drop to `dev`.
# Root itself is never filtered: it is the enforcement plane (this script,
# dnsmasq, the proxy, socat host-forwards) and must stay unrestricted.
#
# Usage: egress-firewall.sh <host-ip-for-host.local>
#
# Reads:
#   CONTAINED_EGRESS_ALLOW  comma-separated allowlist entries (required, non-empty)
#
# Entry syntax:
#   name.example.com       DNS name; matches itself and all subdomains; all ports.
#                           Its 443/80 traffic is routed through the SNI/Host proxy;
#                           other ports (e.g. 22 for git+ssh) are allowed directly.
#   host.local[:PORT]       The host machine (see entrypoint.sh's HOST_IP). Connects
#                           directly, bypassing the proxy. All ports if PORT omitted.
#   IP-OR-CIDR[:PORT]       A literal static destination. Same direct-connect,
#                           all-ports-unless-scoped semantics as host.local.
#
# Fails closed: any setup error aborts (nonzero exit) rather than falling
# back to unfiltered egress; entrypoint.sh treats that as fatal.
set -euo pipefail

HOST_IP="${1:-}"
PROXY_TLS_PORT=18443
PROXY_HTTP_PORT=18080
NFT_RULESET=/run/contained-egress.nft
DNSMASQ_CONF=/run/contained-dnsmasq.conf

log() { echo "egress-firewall: $*" >&2; }
fail() { echo "egress-firewall: error: $*" >&2; exit 1; }

[[ -n "${CONTAINED_EGRESS_ALLOW:-}" ]] || fail "CONTAINED_EGRESS_ALLOW is empty"
command -v nft >/dev/null 2>&1 || fail "nft not found in image"
command -v dnsmasq >/dev/null 2>&1 || fail "dnsmasq not found in image"
command -v python3 >/dev/null 2>&1 || fail "python3 not found in image"

DEV_UID="$(id -u dev)" || fail "could not resolve dev user's uid"

# Capture the upstream resolver(s) currently in effect (from --dns / the
# container runtime's default) before we point resolv.conf at dnsmasq.
upstreams=()
while read -r _kw ns _rest; do
  [[ "$_kw" == "nameserver" ]] && upstreams+=("$ns")
done < /etc/resolv.conf || true
if [[ ${#upstreams[@]} -eq 0 ]]; then
  log "warning: no upstream nameserver found in /etc/resolv.conf; allowlisted" \
      "names will fail to resolve until one is configured (e.g. --dns)."
fi

ipv4_re='^([0-9]{1,3}\.){3}[0-9]{1,3}(/[0-9]{1,2})?$'
hostname_re='^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$'

dns_names=()
# "ip|port" pairs; port is empty string for all-ports entries.
static_entries=()

IFS=',' read -ra raw_entries <<< "$CONTAINED_EGRESS_ALLOW"
for raw in "${raw_entries[@]}"; do
  raw="${raw## }"; raw="${raw%% }"
  [[ -z "$raw" ]] && continue

  host_part="$raw"
  port=""
  if [[ "$raw" == *:* ]]; then
    host_part="${raw%:*}"
    port="${raw##*:}"
    [[ "$port" =~ ^[0-9]+$ && "$port" -ge 1 && "$port" -le 65535 ]] \
      || fail "invalid port in allowlist entry '$raw'"
  fi

  if [[ "$host_part" == "host.local" ]]; then
    [[ -n "$HOST_IP" ]] || fail "host.local requested but host IP is unknown"
    static_entries+=("${HOST_IP}|${port}")
  elif [[ "$host_part" =~ $ipv4_re ]]; then
    static_entries+=("${host_part}|${port}")
  elif [[ "$host_part" == *:* ]]; then
    fail "IPv6 allowlist entries are not supported: '$raw'"
  elif [[ -n "$port" ]]; then
    fail "DNS-name allowlist entries cannot be port-scoped: '$raw' (DNS names are always all-ports)"
  elif [[ "$host_part" =~ $hostname_re ]]; then
    dns_names+=("$host_part")
  else
    fail "unrecognized allowlist entry: '$raw'"
  fi
done

[[ ${#dns_names[@]} -gt 0 || ${#static_entries[@]} -gt 0 ]] || fail "no valid allowlist entries parsed"

# ---- dnsmasq: resolve only allowlisted names, feed their IPs into the nft
# set live, NXDOMAIN everything else. ------------------------------------
{
  echo "no-resolv"
  echo "port=53"
  echo "listen-address=127.0.0.1"
  echo "bind-interfaces"
  for name in "${dns_names[@]+"${dns_names[@]}"}"; do
    for ns in "${upstreams[@]+"${upstreams[@]}"}"; do
      echo "server=/${name}/${ns}"
    done
    echo "nftset=/${name}/4#inet#fw#allow4"
  done
  # Any domain not explicitly routed above falls through to this catch-all;
  # an empty address returns NXDOMAIN (dnsmasq >= 2.87) instead of resolving it.
  echo "address=/#/"
} > "$DNSMASQ_CONF"

# Best-effort ECH hardening: HTTPS/SVCB records can carry an encrypted SNI
# that this proxy cannot inspect. Strip them from allowed answers if this
# dnsmasq build supports it; harmless no-op otherwise.
if dnsmasq --help 2>&1 | grep -q -- '--filter-rr'; then
  echo "filter-rr=HTTPS" >> "$DNSMASQ_CONF"
  echo "filter-rr=SVCB" >> "$DNSMASQ_CONF"
fi

echo "nameserver 127.0.0.1" > /etc/resolv.conf

dnsmasq --conf-file="$DNSMASQ_CONF" --pid-file=/run/contained-dnsmasq.pid \
  || fail "dnsmasq failed to start"

# ---- SNI/Host proxy: the single point that checks 443/80 traffic against
# the DNS-name allowlist by hostname, then dials out by that resolved name
# (never by the client's original destination IP). ------------------------
CONTAINED_ALLOWED_NAMES="$(IFS=,; echo "${dns_names[*]+"${dns_names[*]}"}")" \
CONTAINED_PROXY_TLS_PORT="$PROXY_TLS_PORT" \
CONTAINED_PROXY_HTTP_PORT="$PROXY_HTTP_PORT" \
  python3 /usr/local/bin/sni-proxy.py &
proxy_pid=$!
# Give the proxy a moment to bind before the firewall starts redirecting to it.
for _ in 1 2 3 4 5 6 7 8 9 10; do
  kill -0 "$proxy_pid" 2>/dev/null || fail "sni-proxy.py exited during startup"
  ss -ltn 2>/dev/null | grep -q ":${PROXY_TLS_PORT} " && break
  sleep 0.2
done

# ---- nftables: default-deny for `dev`; root and other system users are
# unfiltered (they are the enforcement plane, never adversarial in this
# model). --------------------------------------------------------------
bypass_ips=()
allow4_ips=()
port_rules=()
for entry in "${static_entries[@]+"${static_entries[@]}"}"; do
  ip="${entry%|*}"
  eport="${entry#*|}"
  bypass_ips+=("$ip")
  if [[ -n "$eport" ]]; then
    port_rules+=("ip daddr ${ip} tcp dport ${eport} accept")
  else
    allow4_ips+=("$ip")
  fi
done

join_by_comma() { local IFS=,; echo "$*"; }

{
  echo "flush ruleset"
  echo
  echo "table ip nat {"
  echo "  set bypass4 {"
  echo "    type ipv4_addr; flags interval;"
  if [[ ${#bypass_ips[@]} -gt 0 ]]; then
    echo "    elements = { $(join_by_comma "${bypass_ips[@]}") }"
  fi
  echo "  }"
  echo "  chain output {"
  echo "    type nat hook output priority -100; policy accept;"
  echo "    meta skuid ${DEV_UID} tcp dport 443 ip daddr != @bypass4 counter redirect to :${PROXY_TLS_PORT}"
  echo "    meta skuid ${DEV_UID} tcp dport 80  ip daddr != @bypass4 counter redirect to :${PROXY_HTTP_PORT}"
  echo "  }"
  echo "}"
  echo
  echo "table inet fw {"
  echo "  set allow4 {"
  echo "    type ipv4_addr; flags interval;"
  if [[ ${#allow4_ips[@]} -gt 0 ]]; then
    echo "    elements = { $(join_by_comma "${allow4_ips[@]}") }"
  fi
  echo "  }"
  echo "  chain output {"
  echo "    type filter hook output priority 0; policy drop;"
  echo "    meta skuid != ${DEV_UID} accept"
  echo "    ct state established,related accept"
  echo "    ip daddr 127.0.0.1 udp dport 53 accept"
  echo "    ip daddr 127.0.0.1 tcp dport 53 accept"
  echo "    ip daddr 127.0.0.1 tcp dport ${PROXY_TLS_PORT} accept"
  echo "    ip daddr 127.0.0.1 tcp dport ${PROXY_HTTP_PORT} accept"
  echo "    udp dport 443 counter log prefix \"contained-fw-blocked-quic: \" reject"
  for rule in "${port_rules[@]+"${port_rules[@]}"}"; do
    echo "    ${rule}"
  done
  echo "    ip daddr @allow4 accept"
  echo "    meta l4proto tcp counter log prefix \"contained-fw-blocked: \" reject with tcp reset"
  echo "    meta l4proto udp counter log prefix \"contained-fw-blocked: \" reject with icmp port-unreachable"
  echo "    counter log prefix \"contained-fw-blocked: \" reject"
  echo "  }"
  echo "}"
} > "$NFT_RULESET"

nft -f "$NFT_RULESET" || fail "failed to load nftables ruleset"

log "egress allowlist active:" \
    "$( [[ ${#dns_names[@]} -gt 0 ]] && printf '%s (+subdomains) ' "${dns_names[@]}" )" \
    "$( [[ ${#static_entries[@]} -gt 0 ]] && printf '%s ' "${static_entries[@]/|/:}" )"
