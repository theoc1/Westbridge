# Westbridge

Web control panel for a single Asterisk **ConfBridge** audio conference. Three things,
deliberately no more:

- a **live participant list** that updates as calls join and leave;
- **kick** a participant;
- **add** a participant by dialing a phone number.

The whole application ships as one Go binary with the React frontend embedded in it.
There is nothing to deploy alongside it and no database.

```
browser  --HTTP/JSON--> Go backend --AMI TCP 5038--> Asterisk
        <--WebSocket---            (single connection)
```

## How it talks to Asterisk

The backend holds exactly **one** persistent AMI TCP connection and uses it for both the
event stream and the commands:

| Purpose | AMI |
| --- | --- |
| Live roster | events `ConfbridgeJoin`, `ConfbridgeLeave`, `ConfbridgeStart`, `ConfbridgeEnd` |
| Snapshot / resync | action `ConfbridgeList` |
| Kick | action `ConfbridgeKick` |
| Add a participant | action `Originate` (`Async: true`) |

The WebSocket in this app lives between the **browser and this backend**, not between the
backend and Asterisk. It is one-way, server to browser, and carries state only; commands
travel over ordinary REST. Every state change broadcasts a *full* snapshot — a conference
holds tens of participants at most, so a snapshot is a few hundred bytes and an entire
class of desync bugs simply cannot occur.

### Why not AMI over WebSocket

The original idea was to reach Asterisk over a WebSocket to avoid keeping two interfaces
open. **Asterisk does not support this.** Checked against `asterisk/asterisk` master:

- `main/manager.c` contains **zero** references to websocket, and there is no
  `res_ami_websocket` module in the tree. The only WebSocket modules are
  `res_http_websocket`, `res_websocket_client`, `res_pjsip_transport_websocket` and
  `chan_websocket` (media).
- AMI therefore has exactly two transports: raw TCP/TLS on 5038, and AMI-over-HTTP
  (`/manager`, `/rawman`, `/mxml`) when `manager.conf` sets `webenabled=yes`. The HTTP
  variant delivers events by long-poll (`/rawman?action=waitevent`), not by WebSocket.
- WebSocket in Asterisk exists only for ARI events, WebRTC signalling and `chan_websocket`.

The obvious alternative, ARI (which *does* have a real event WebSocket at `/ari/events`),
cannot see these conferences at all: `/bridges` only exposes bridges created by ARI/Stasis,
so a conference started from the dialplan with `ConfBridge()` is invisible to it.
Participant lists and control for ConfBridge are available **only** through AMI.

So a single AMI TCP connection carries everything — which is one Asterisk interface, fewer
than the two the original design assumed.

### Staying correct

- **Recovery:** on every AMI (re)connect and on a `WB_RESYNC_INTERVAL` timer, the backend
  re-runs `ConfbridgeList` and rebuilds the roster from scratch, so missed events cannot
  accumulate into drift.
- **Link state is visible:** when AMI is down the API reports `asteriskConnected: false`
  and the UI greys the roster out and says so, rather than showing frozen data as if it
  were live.
- **The backend starts and stays up when Asterisk is unreachable.** A conference control
  panel that refuses to boot because the PBX is down is exactly the tool you cannot use
  when the PBX is down.

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

`make build` writes the Vite output into `internal/web/assets/dist`, which the Go binary
embeds. That directory keeps a committed `.gitkeep` so `go build` works on a fresh clone
before the frontend has ever been built; a binary built that way serves the API and
reports that no frontend is embedded instead of serving a blank page.

The resulting `.bin/westbridge` is self-contained — copy it anywhere, no `frontend/`
directory required at runtime.

## Running

Set the required variables and start it:

```sh
export WB_AMI_USER=westbridge WB_AMI_SECRET=westbridge-secret
export WB_ROOM=1000 WB_ORIGINATE_CONTEXT=conference-out
.bin/westbridge
```

Then open <http://localhost:8080>.

### Configuration

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

There is **no authentication**: this is an MVP meant for a trusted network or a VPN. Do not
publish it to the internet as it stands — anyone who can reach it can drop calls and place
outbound ones.

The AMI user needs `read = system,call,reporting` and `write = system,call,originate`;
see `deploy/asterisk/manager.conf` for a working example.

### HTTP API

```
GET    /api/conference                         -> 200 {"room":"1000","asteriskConnected":true,"participants":[...]}
POST   /api/conference/participants            body {"number":"1002"} -> 202 {"actionId":"..."}
DELETE /api/conference/participants/{uniqueid} -> 204
GET    /ws                                     -> WebSocket, server -> client only
```

Errors come back as `{"error":"..."}`. A participant looks like:

```json
{
  "uniqueid": "1756...", "channel": "PJSIP/1001-0000000a",
  "callerIdNum": "1001", "callerIdName": "Alice",
  "admin": false, "muted": false, "joinedAt": "2026-09-01T10:00:00Z"
}
```

The WebSocket sends the current snapshot immediately on connect and then one message per
change: `{"type":"snapshot","room":"1000","asteriskConnected":true,"participants":[...]}`.

## Local Asterisk test stand

`deploy/` contains a docker-compose stand with a real Asterisk: ConfBridge room **1000**
and two softphone endpoints, **1001** and **1002**.

```sh
docker compose -f deploy/docker-compose.yml up -d --build
cp deploy/.env.example deploy/.env
set -a; . ./deploy/.env; set +a
make build && .bin/westbridge
```

Register a softphone as `1001` (password `1001-secret`) against `127.0.0.1:5060` over UDP
and dial `1000`. See **[deploy/README.md](deploy/README.md)** for endpoint details, CLI
checks, and the invite flow.

Note that **RTP through Docker on macOS is unreliable** — SIP signalling and the roster
work, audio often does not. This app cares about signalling and roster state, so that is
expected on the stand and not worth debugging.

## Frontend development

`npm run dev` proxies `/api` and `/ws` to the Go backend on `:8080`, so run the binary and
Vite side by side and get hot reload:

```sh
.bin/westbridge &
cd frontend && npm run dev
```

## Layout

```
cmd/westbridge/main.go     entry point, config, wiring, graceful shutdown
internal/ami/              AMI protocol codec + client (connection, login, actions, events)
internal/ami/amitest/      fake AMI server used by the tests
internal/conference/       roster state + service (list / kick / invite)
internal/hub/              browser WebSocket pub/sub
internal/web/              HTTP handlers, WS endpoint, embedded assets
frontend/                  React + Vite + TypeScript sources
deploy/                    docker-compose stand + Asterisk configs
docs/plans/                the implementation plan and the research behind it
```
