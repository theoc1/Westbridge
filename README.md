# Westbridge

Web control panel for a single Asterisk **ConfBridge** audio conference: a live
participant list, kicking a participant, and adding one by dialing a phone number.

The backend is a single Go binary with the frontend embedded; it holds exactly one
persistent AMI TCP connection to Asterisk and uses it for both the event stream and
commands. The WebSocket lives between the browser and this backend — Asterisk itself
does **not** speak AMI over WebSocket (see [docs/plans](docs/plans) for the research
behind that decision).

> Status: under construction. See `docs/plans/20260901-asterisk-conference-web-mvp.md`.

## Prerequisites

| Tool | Version | Notes |
| --- | --- | --- |
| Go | **1.22 or newer** | Required for `net/http` method routing patterns and `go:embed`. Verified on 1.27. |
| Node.js | 20.19+ (24 LTS recommended) | Vite 8 will not run on older releases. Verified on 26.8. |
| Docker | any recent Desktop / Engine | Only needed for the local Asterisk test stand under `deploy/`. |

Check what you have:

```sh
go version
node -v
docker compose version
```

### Docker CLI missing on macOS

Docker Desktop can be installed while `docker` is absent from `PATH` — the binaries
live inside the app bundle. Link them once:

```sh
sudo ln -sf /Applications/Docker.app/Contents/Resources/bin/docker /usr/local/bin/docker
```

The `compose` subcommand is a CLI plugin and is picked up automatically from
`~/.docker/cli-plugins`, so `docker compose version` works as soon as `docker` resolves.
Docker Desktop must be running for any command to connect.

## Building and testing

```sh
make          # lint, test, build
make build    # frontend (Vite) + go build -o .bin/westbridge ./cmd/westbridge
make test     # go test ./...
make lint     # golangci-lint + tsc --noEmit
make tools    # install golangci-lint into .bin/
```

`make build` writes the Vite output into `internal/web/assets/dist`, which the Go
binary embeds. That directory keeps a committed `.gitkeep` so `go build` works on a
fresh clone before the frontend has ever been built.

## Configuration

All configuration comes from the environment.

| Variable | Default | Meaning |
| --- | --- | --- |
| `WB_LISTEN` | `:8080` | HTTP listen address |
| `WB_AMI_ADDR` | `127.0.0.1:5038` | Asterisk AMI address |
| `WB_AMI_USER` | — | AMI username (**required**) |
| `WB_AMI_SECRET` | — | AMI secret (**required**) |
| `WB_ROOM` | — | ConfBridge room number (**required**) |
| `WB_ORIGINATE_CONTEXT` | — | Dialplan context for outbound calls (**required**) |
| `WB_ORIGINATE_CALLERID` | `Westbridge <0000>` | Caller ID for originated calls |
| `WB_ORIGINATE_TIMEOUT` | `30s` | Dial timeout (sent to AMI as milliseconds) |
| `WB_RESYNC_INTERVAL` | `30s` | Periodic full-roster resync |

Missing required variables are reported together and the process exits non-zero.
