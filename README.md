# syslog-ng-relp

RELP forwarder for [syslog-ng OSE](https://github.com/syslog-ng/syslog-ng) — a single Go binary with zero external dependencies that reads log lines from stdin and delivers them reliably via the [RELP protocol](https://www.rsyslog.com/doc/v8-stable/configuration/modules/imrelp.html).

Designed for syslog-ng's `program()` destination driver, enabling RELP output without compiling syslog-ng with librelp support.

## Features

- **Pure Go RELP v1 implementation** — no librelp, no CGO, no external dependencies
- **Reliable delivery** — every message is acked by the RELP server before proceeding
- **Automatic reconnection** — configurable retry delay on connection loss
- **TLS support** — optional TLS with certificate verification skip
- **1 MB line buffer** — handles long syslog messages
- **Single static binary** — runs from scratch Docker images
- **Cross-platform** — builds for linux/darwin on amd64/arm64

## Installation

### From source

```bash
go install github.com/cybericius/syslog-ng-relp@latest
```

### From release binary

Download from [GitHub Releases](https://github.com/cybericius/syslog-ng-relp/releases) and place in `/usr/local/bin/`.

### Docker

```bash
docker pull ghcr.io/cybericius/syslog-ng-relp:latest
```

### Build from source

```bash
git clone https://github.com/cybericius/syslog-ng-relp.git
cd syslog-ng-relp
make build
```

## Usage with syslog-ng

Add a `program()` destination to your syslog-ng configuration:

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

### Docker: custom syslog-ng image

Add the forwarder to your syslog-ng Docker image:

```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /build
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o relp-forwarder .

FROM balabit/syslog-ng:4.10.2
COPY --from=builder /build/relp-forwarder /usr/local/bin/relp-forwarder
```

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `localhost` | RELP server hostname |
| `--port` | `2514` | RELP server port |
| `--tls` | `false` | Enable TLS encryption |
| `--tls-insecure` | `false` | Skip TLS certificate verification |
| `--reconnect-delay` | `2s` | Delay between reconnection attempts |
| `--version` | | Print version and exit |

## How it works

```
┌──────────┐  stdin   ┌─────────────────┐  RELP/TCP  ┌──────────────┐
│ syslog-ng │────────►│ relp-forwarder  │───────────►│ RELP server  │
│ program() │         │ (this binary)   │◄───────────│ (rsyslog,    │
└──────────┘         └─────────────────┘   RELP ACK  │  etc.)       │
                                                      └──────────────┘
```

1. syslog-ng writes one log line per `write()` to the program's stdin
2. The forwarder opens a RELP session (handshake with `open` command)
3. Each line is sent as a `syslog` command and the forwarder waits for an ack (`rsp 200 OK`)
4. On connection failure, the forwarder reconnects and retries the failed message
5. On stdin EOF (syslog-ng shutdown), the forwarder sends `close` and exits

## Why not `network(transport("relp"))`?

syslog-ng's built-in RELP transport requires compiling with librelp support. Many distribution packages and the official Docker image (`balabit/syslog-ng`) don't include it. The `program()` approach works with any syslog-ng installation — just drop the binary in and configure.

## Requirements

- Go 1.24+ (build only)
- A RELP-capable receiver (rsyslog `imrelp`, or any RFC RELP server)

## License

MIT — see [LICENSE](LICENSE).
