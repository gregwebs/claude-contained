#!/usr/bin/env python3
"""Root-owned SNI/Host passthrough proxy for the egress allowlist firewall.

nftables redirects the `dev` user's outbound TCP 443/80 (except destinations
explicitly excluded as static bypass entries) to this proxy's two loopback
listeners. For each connection, the proxy reads just enough of the client's
first bytes to learn the intended hostname -- the SNI in a TLS ClientHello
for 443, the Host header for 80 -- checks that name against the allowlist,
and if permitted, dials *that name* (not the client's original destination)
to make its own upstream connection, resolving via the system resolver
(dnsmasq, which only answers allowlisted names). This is what actually closes
the shared-CDN-IP bypass: an allowlisted name's IP can't be reused to reach a
different, non-allowlisted name, because the proxy never trusts the IP the
client tried to connect to -- only the name it presented, and only once that
name resolves independently through the same allowlist. No TLS interception
is performed; bytes are tunneled as-is once the destination is approved, so
certificate validation still happens end-to-end between the real client and
the real server. Connections with no parseable name are rejected outright.

Invoked by egress-firewall.sh; not meant to be run standalone.
"""
import asyncio
import os
import sys

ALLOWED = [
    n.strip().lower()
    for n in os.environ.get("CONTAINED_ALLOWED_NAMES", "").split(",")
    if n.strip()
]
TLS_PORT = int(os.environ.get("CONTAINED_PROXY_TLS_PORT", "18443"))
HTTP_PORT = int(os.environ.get("CONTAINED_PROXY_HTTP_PORT", "18080"))

MAX_PEEK = 16 * 1024
HANDSHAKE_TIMEOUT = 5.0
CONNECT_TIMEOUT = 8.0


def log(msg):
    print(f"sni-proxy: {msg}", file=sys.stderr, flush=True)


def is_allowed(name):
    name = name.lower().rstrip(".")
    for allowed in ALLOWED:
        if name == allowed or name.endswith("." + allowed):
            return True
    return False


class NeedMore(Exception):
    pass


def parse_sni(buf):
    """Return the SNI hostname from a (possibly partial) TLS ClientHello.

    Raises NeedMore if more bytes are needed, ValueError if what's present
    is malformed or clearly not a single-record ClientHello (fragmented
    ClientHellos are rare in practice and are treated as unparseable here,
    which fails closed -- the connection is rejected, not let through)."""
    if len(buf) < 5:
        raise NeedMore()
    if buf[0] != 0x16:
        raise ValueError("not a TLS handshake record")
    record_len = int.from_bytes(buf[3:5], "big")
    if len(buf) < 5 + record_len:
        raise NeedMore()
    body = buf[5 : 5 + record_len]
    if len(body) < 4 or body[0] != 0x01:
        raise ValueError("not a ClientHello")
    hs_len = int.from_bytes(body[1:4], "big")
    hello = body[4 : 4 + hs_len]
    if len(hello) < hs_len:
        raise ValueError("ClientHello split across TLS records")

    pos = 2 + 32  # client_version + random
    if len(hello) < pos + 1:
        raise ValueError("truncated ClientHello")
    sid_len = hello[pos]
    pos += 1 + sid_len
    if len(hello) < pos + 2:
        raise ValueError("truncated ClientHello")
    cs_len = int.from_bytes(hello[pos : pos + 2], "big")
    pos += 2 + cs_len
    if len(hello) < pos + 1:
        raise ValueError("truncated ClientHello")
    cm_len = hello[pos]
    pos += 1 + cm_len
    if len(hello) < pos + 2:
        raise ValueError("ClientHello has no extensions (no SNI)")
    ext_total_len = int.from_bytes(hello[pos : pos + 2], "big")
    pos += 2
    ext_end = pos + ext_total_len
    if len(hello) < ext_end:
        raise ValueError("truncated extensions")

    while pos + 4 <= ext_end:
        ext_type = int.from_bytes(hello[pos : pos + 2], "big")
        ext_len = int.from_bytes(hello[pos + 2 : pos + 4], "big")
        ext_data = hello[pos + 4 : pos + 4 + ext_len]
        if ext_type == 0x0000:  # server_name
            if len(ext_data) < 2:
                raise ValueError("malformed SNI extension")
            list_len = int.from_bytes(ext_data[0:2], "big")
            entries = ext_data[2 : 2 + list_len]
            epos = 0
            while epos + 3 <= len(entries):
                name_type = entries[epos]
                name_len = int.from_bytes(entries[epos + 1 : epos + 3], "big")
                name = entries[epos + 3 : epos + 3 + name_len]
                if name_type == 0:
                    return name.decode("ascii", "ignore")
                epos += 3 + name_len
            raise ValueError("SNI extension present but empty")
        pos += 4 + ext_len
    raise ValueError("no SNI extension present")


def parse_http_host(buf):
    """Return the Host header value from a (possibly partial) HTTP request."""
    idx = buf.find(b"\r\n\r\n")
    if idx == -1:
        if len(buf) > MAX_PEEK:
            raise ValueError("request headers too large without a Host header")
        raise NeedMore()
    head = buf[:idx]
    for line in head.split(b"\r\n")[1:]:
        if line.lower().startswith(b"host:"):
            host = line.split(b":", 1)[1].strip().decode("ascii", "ignore")
            return host.split(":")[0]  # strip :port if present
    raise ValueError("no Host header present")


async def peek_and_parse(reader, parser):
    """Read from reader until parser succeeds; return (hostname, raw_bytes_read)."""
    buf = b""
    loop = asyncio.get_event_loop()
    deadline = loop.time() + HANDSHAKE_TIMEOUT
    while True:
        try:
            return parser(buf), buf
        except NeedMore:
            pass
        if len(buf) > MAX_PEEK:
            raise ValueError("too much data without a complete hello")
        remaining = deadline - loop.time()
        if remaining <= 0:
            raise ValueError("timed out waiting for a complete hello")
        chunk = await asyncio.wait_for(reader.read(4096), timeout=remaining)
        if not chunk:
            raise ValueError("connection closed before hello completed")
        buf += chunk


async def pipe(reader, writer):
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                break
            writer.write(data)
            await writer.drain()
    except (ConnectionResetError, BrokenPipeError, asyncio.IncompleteReadError):
        pass
    finally:
        try:
            writer.close()
        except Exception:
            pass


async def handle(reader, writer, parser, upstream_port):
    peer = writer.get_extra_info("peername")
    try:
        hostname, buffered = await peek_and_parse(reader, parser)
    except Exception as e:
        log(f"blocked {peer}: {e}")
        writer.close()
        return

    if not is_allowed(hostname):
        log(f"blocked {peer}: '{hostname}' is not in the allowlist")
        writer.close()
        return

    try:
        upstream_reader, upstream_writer = await asyncio.wait_for(
            asyncio.open_connection(hostname, upstream_port), timeout=CONNECT_TIMEOUT
        )
    except Exception as e:
        log(f"blocked {peer}: could not connect to '{hostname}': {e}")
        writer.close()
        return

    log(f"allowed {peer} -> {hostname}:{upstream_port}")
    upstream_writer.write(buffered)
    await upstream_writer.drain()

    await asyncio.gather(
        pipe(reader, upstream_writer),
        pipe(upstream_reader, writer),
    )


async def main():
    if not ALLOWED:
        log("no allowed names configured; every connection will be blocked")

    tls_server = await asyncio.start_server(
        lambda r, w: handle(r, w, parse_sni, 443), "127.0.0.1", TLS_PORT
    )
    http_server = await asyncio.start_server(
        lambda r, w: handle(r, w, parse_http_host, 80), "127.0.0.1", HTTP_PORT
    )
    log(f"listening on 127.0.0.1:{TLS_PORT} (TLS/SNI) and 127.0.0.1:{HTTP_PORT} (HTTP/Host)")
    async with tls_server, http_server:
        await asyncio.gather(tls_server.serve_forever(), http_server.serve_forever())


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        pass
