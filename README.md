# syslog-ng-relp

RELP tools for [syslog-ng OSE](https://github.com/syslog-ng/syslog-ng) — pure Go binaries with zero external dependencies that add RELP support to any syslog-ng installation via the `program()` driver.

**Two binaries:**
- **relp-forwarder** — reads from stdin, sends via RELP (for `program()` destination)
- **relp-listener** — accepts RELP connections, writes to stdout (for `program()` source)

No librelp, no CGO, no recompilation of syslog-ng required.

## Features

- **Pure Go RELP v1 implementation** — no librelp, no CGO, no external dependencies
- **Reliable delivery** — every message is acked before proceeding
- **Automatic reconnection** — configurable retry delay on connection loss (forwarder)
- **Concurrent connections** — listener handles multiple RELP clients simultaneously
- **TLS support** — both binaries support TLS (listener with cert/key, forwarder with optional insecure skip)
- **1 MB line buffer** — handles long syslog messages
- **Single static binaries** — run from scratch Docker images
- **Cross-platform** — builds for linux/darwin on amd64/arm64

## Installation

### From source

```bash
go install github.com/cybericius/syslog-ng-relp/cmd/relp-forwarder@latest
go install github.com/cybericius/syslog-ng-relp/cmd/relp-listener@latest
```

### From release binary

Download from [GitHub Releases](https://github.com/cybericius/syslog-ng-relp/releases) and place in `/usr/local/bin/`.

### Docker

```bash
docker pull ghcr.io/cybericius/syslog-ng-relp:latest
```

Both binaries are included in the image. Override the entrypoint to use the listener:

```bash
docker run ghcr.io/cybericius/syslog-ng-relp:latest /relp-listener --port=2514
```

### Build from source

```bash
git clone https://github.com/cybericius/syslog-ng-relp.git
cd syslog-ng-relp
make build
```

## relp-forwarder (destination)

Reads log lines from stdin and delivers them to a remote RELP server. Use with syslog-ng's `program()` destination:

```
destination d_relp {
    program("/usr/local/bin/relp-forwarder --host=rsyslog.example.com --port=2514"
        persist-name("d_relp")
    );
};

log {
    source(s_local);
    destination(d_relp);
};
```

### With TLS

```
destination d_relp_tls {
    program("/usr/local/bin/relp-forwarder --host=secure.example.com --port=6514 --tls"
        persist-name("d_relp_tls")
    );
};
```

### Forwarder flags

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `localhost` | RELP server hostname |
| `--port` | `2514` | RELP server port |
| `--tls` | `false` | Enable TLS encryption |
| `--tls-insecure` | `false` | Skip TLS certificate verification |
| `--reconnect-delay` | `2s` | Delay between reconnection attempts |
| `--version` | | Print version and exit |

### How the forwarder works

```
┌──────────┐  stdin   ┌─────────────────┐  RELP/TCP  ┌──────────────┐
│ syslog-ng │────────►│ relp-forwarder  │───────────►│ RELP server  │
│ program() │         │                 │◄───────────│ (rsyslog,    │
└──────────┘         └─────────────────┘   RELP ACK  │  etc.)       │
                                                      └──────────────┘
```

## relp-listener (source)

Accepts incoming RELP connections and writes received syslog messages to stdout, one per line. Use with syslog-ng's `program()` source:

```
source s_relp {
    program("/usr/local/bin/relp-listener --port=2514"
        persist-name("s_relp")
    );
};

log {
    source(s_relp);
    destination(d_local);
};
```

### With TLS

```
source s_relp_tls {
    program("/usr/local/bin/relp-listener --port=6514 --tls --tls-cert=/etc/ssl/server.crt --tls-key=/etc/ssl/server.key"
        persist-name("s_relp_tls")
    );
};
```

### Listener flags

| Flag | Default | Description |
|------|---------|-------------|
| `--listen` | `0.0.0.0` | Listen address |
| `--port` | `2514` | Listen port |
| `--tls` | `false` | Enable TLS encryption |
| `--tls-cert` | | TLS certificate file (required with --tls) |
| `--tls-key` | | TLS private key file (required with --tls) |
| `--version` | | Print version and exit |

### How the listener works

```
┌──────────────┐  RELP/TCP  ┌─────────────────┐  stdout  ┌──────────┐
│ RELP client  │───────────►│ relp-listener   │────────►│ syslog-ng │
│ (rsyslog,    │◄───────────│                 │         │ program() │
│  etc.)       │   RELP ACK └─────────────────┘         └──────────┘
└──────────────┘
```

1. The listener binds to a TCP port and accepts RELP connections
2. For each connection, it performs the RELP handshake (`open` command)
3. Each `syslog` command is acked (`rsp 200 OK`) and the message is written to stdout
4. syslog-ng reads lines from the program's stdout
5. On `close` command, the connection is cleanly terminated
6. Multiple concurrent RELP clients are supported

## Docker: custom syslog-ng image

Add both tools to your syslog-ng Docker image:

```dockerfile
FROM golang:1.24-alpine AS relp-builder
WORKDIR /build
COPY --from=ghcr.io/cybericius/syslog-ng-relp:latest /relp-forwarder /relp-forwarder
COPY --from=ghcr.io/cybericius/syslog-ng-relp:latest /relp-listener /relp-listener

FROM balabit/syslog-ng:4.10.2
COPY --from=relp-builder /relp-forwarder /usr/local/bin/relp-forwarder
COPY --from=relp-builder /relp-listener /usr/local/bin/relp-listener
```

## Comparison with syslog-ng built-in RELP

syslog-ng has a built-in `network(transport("relp"))` driver. Here's how it compares:

| | **Built-in `transport("relp")`** | **syslog-ng-relp (this project)** |
|---|---|---|
| **Dependency** | Requires librelp (C library) linked at compile time | Pure Go, zero external dependencies |
| **Availability** | Missing from most distro packages and the official Docker image (`balabit/syslog-ng`) | Drop-in binary — works with any syslog-ng |
| **Installation** | Recompile syslog-ng with `--enable-relp` or find a package that includes it | Copy binary to `/usr/local/bin/`, add `program()` config |
| **TLS** | Via librelp + GnuTLS | Native Go TLS (no GnuTLS dependency) |
| **Protocol** | RELP v1 via librelp | RELP v1, pure Go implementation |
| **Direction** | Source and destination drivers | Both: `relp-listener` (source) and `relp-forwarder` (destination) |
| **Reconnection** | Built-in with `time-reopen()` | Automatic with configurable delay (default 2s) |
| **Concurrency** | Single-threaded per driver instance | Listener handles multiple concurrent RELP clients |
| **Integration** | Native `source{}` / `destination{}` blocks | Via `program()` driver (stdin/stdout) |
| **Buffering** | syslog-ng disk/memory buffer | syslog-ng `program()` buffer + 1MB line buffer |
| **Container size** | Adds ~2MB (librelp + GnuTLS) to syslog-ng image — if you can build it | ~6MB static binary, works with stock images |

**When to use the built-in driver:** If your syslog-ng package already includes librelp support, the native driver avoids the `program()` indirection and integrates directly with syslog-ng's internal buffering.

**When to use this project:** If you're running the official Docker image, a distro package without RELP, or want to avoid recompiling syslog-ng. Drop the binaries in and you're done — no build toolchain, no library dependencies.

## Requirements

- Go 1.24+ (build only)
- A RELP-capable peer (rsyslog `imrelp`/`omrelp`, or any RELP v1 implementation)

## License

MIT — see [LICENSE](LICENSE).
