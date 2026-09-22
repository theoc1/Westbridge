# Westbridge

Web control panel for a single Asterisk **ConfBridge** audio conference. Three things,
deliberately no more:

- a **live participant list** that updates as calls join and leave;
- **kick** a participant;
- **add** participants by dialing multiple numbers, with per-call cancellation and retry.

The whole application ships as one Go binary with the React frontend embedded in it.
Users and revocable sessions are stored in a local SQLite file; no separate database server is needed.

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
# Local development over HTTP only:
export WB_LISTEN=127.0.0.1:8080 WB_COOKIE_SECURE=false
.bin/westbridge bootstrap-admin admin  # prompts for a password, once
.bin/westbridge
```

Then open <http://localhost:8080>.

### Configuration

All configuration comes from the environment.

| Variable | Default | Meaning |
| --- | --- | --- |
| `WB_LISTEN` | `:8080` | HTTP listen address |
| `WB_DB_PATH` | `data/westbridge.db` | SQLite users, sessions and contacts file, relative to the working directory |
| `WB_COOKIE_SECURE` | `true` | HTTPS-only cookies; set `false` for local HTTP development |
| `WB_AMI_ADDR` | `127.0.0.1:5038` | Asterisk AMI address |
| `WB_AMI_USER` | — | AMI username (**required**) |
| `WB_AMI_SECRET` | — | AMI secret (**required**) |
| `WB_ROOM` | — | ConfBridge room number (**required**) |
| `WB_ORIGINATE_CONTEXT` | — | Dialplan context for outbound calls (**required**) |
| `WB_ORIGINATE_CALLERID` | `Westbridge <0000>` | Caller ID for originated calls |
| `WB_ORIGINATE_TIMEOUT` | `30s` | Dial timeout (sent to AMI as milliseconds) |
| `WB_RESYNC_INTERVAL` | `30s` | Periodic full-roster resync |
| `WB_ALLOWED_ORIGINS` | — | Comma-separated extra `Origin` hosts accepted on `/ws` |

Missing required variables are reported together and the process exits non-zero.

### Users and sign-in

Run `.bin/westbridge bootstrap-admin admin` from the same working directory and
with the same `WB_DB_PATH` as the server. It prompts twice for a password without
showing it and only works on an empty user database. There are no default credentials.
Passwords must have at least 3 characters (at most 1024 UTF-8 bytes). Logins are
case insensitive and accept ASCII letters, digits, dots, underscores and hyphens.

Sign in, then open **Users** to create accounts, change roles, reset passwords or
disable users. Both roles can view, invite and kick conference participants; only
administrators can manage accounts. The last active administrator cannot be disabled
or demoted. Disabling an account keeps its stable ID for future user-owned settings.
User settings are not part of this iteration.

Authentication uses opaque, cryptographically random session tokens in **HttpOnly,
SameSite=Strict** cookies. Tokens are stored only as SHA-256 hashes in SQLite;
passwords use Argon2id (19 MiB, two passes, one lane), following the
[OWASP password storage guidance](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html).
Sessions have a fixed 12-hour lifetime and survive server restarts. Signing out
revokes that session. Any administrator change to an account revokes all of its
sessions; existing WebSockets check validity before sending data and every second.

Serve production behind **HTTPS**, leaving `WB_COOKIE_SECURE=true`. For local HTTP
on loopback, use `WB_COOKIE_SECURE=false` (already set in `deploy/.env.example`).
Keep the SQLite file in a persistent writable directory and include it in backups;
the application creates it with owner-only permissions. The file contains users,
password hashes, sessions and personal contacts, and is ignored by Git under `data/`.

All conference API routes and `/ws` require a session. Mutating API calls require
`Content-Type: application/json` and reject foreign browser origins; `WB_ALLOWED_ORIGINS`
can allow trusted development origins. Login attempts are limited to ten per minute
per direct client IP, with at most four password checks running concurrently. Behind
a reverse proxy this limit applies to the proxy IP; forwarded IP headers are not trusted.

The AMI user needs `read = system,call,reporting` and `write = system,call,originate,reporting`;
see `deploy/asterisk/manager.conf` for a working example.

### Personal phonebook

Each user has a private phonebook stored in the same SQLite database, including
administrators. The panel appears to the left of the conference (above it on narrow
screens). Add, edit or delete a name and number, select contacts and use **Call selected**
to enqueue calls into the current shared conference without waiting for answers.
Accepted entries are deselected; request failures remain selected with an error.
Call outcomes appear in the conference's colored rows as usual.

Names from your book override the displayed caller name for matching numbers,
including outgoing attempts. These labels stay personal; the shared conference
snapshot never contains another user's contact book. Formatted numbers are normalized,
and a number can appear only once per user's book. Contacts survive server restarts.
Other tabs reload the book on focus. Deleting a contact does not end its active call.

### HTTP API

```
POST   /api/auth/login                         body {"login":"...","password":"..."} -> user + session cookie
GET    /api/auth/me                            -> current user
POST   /api/auth/logout                        -> 204, revokes session
GET    /api/users                              -> users (admin only)
POST   /api/users                              body {"login":"...","password":"...","role":"user"} -> 201 (admin)
PATCH  /api/users/{id}                          body {"role":"user","enabled":false,"password":"..."} (all fields optional, admin)
GET    /api/contacts                           -> current user's [{"id":1,"name":"Alice","number":"1002"}]
POST   /api/contacts                           body {"name":"Alice","number":"1002"} -> 201
PUT    /api/contacts/{id}                       body {"name":"Alice","number":"1003"} -> 200
DELETE /api/contacts/{id}                       -> 204 (owner only, including admins)
GET    /api/conference                         -> 200 {"room":"1000","asteriskConnected":true,"participants":[...],"calls":[...]}
POST   /api/conference/participants            body {"number":"1002"} -> 202 {"actionId":"..."}
DELETE /api/conference/participants/{uniqueid} -> 204
DELETE /api/conference/calls/{id}               -> 204 (cancel a dial or remove a failed row)
POST   /api/conference/calls/{id}/retry          -> 202 {"actionId":"..."}
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

`joinedAt` is omitted when the participant was first discovered by a snapshot;
Asterisk reports call age, not time in the conference. The UI shows “—” in that case.

The WebSocket sends the current snapshot immediately on connect and then one message per
change: `{"type":"snapshot","room":"1000","asteriskConnected":true,"participants":[...],"calls":[...]}`.

### Outgoing call states

The form is ready for another number as soon as the server accepts the attempt;
it does not wait for an answer. Everyone viewing the conference sees the same calls:

- Green: a participant has actually joined ConfBridge; **Kick** removes them.
- Yellow: an independent dial attempt is in progress; **Cancel** requests a real
  AMI Hangup and stays in “Cancelling” until the channel is gone.
- Red: the call failed, with a reason such as Busy, No answer, Unavailable or
  Connection failed; **Retry** starts a new attempt and **Remove** dismisses it.

Snapshots include `calls`, an array of `{id, number, state, reason?, createdAt,
cancelling?}`. `state` is `dialing` or `failed`; connected calls appear only in
`participants`. Async Originate acceptance creates an attempt even if the later
outcome is a failure. An ambiguous network error remains pending until Asterisk
confirms an outcome; it is never treated as proof that no call exists.

Westbridge sets `ChannelId` and `OtherChannelId` and uses `Local/.../n` so that
channel IDs remain stable. Correlation and cancellation use those IDs, not phone
numbers. If an answer races with Cancel, the same outgoing channel is hung up.
DialEnd, Hangup and OriginateResponse supply failure reasons; the most specific
available reason wins. Ordinary AMI outcomes are delivered to the event stream
even when the command acknowledgement is still pending.

Call rows are transient, shared in-memory state, not call history: they reset on a
Westbridge restart. Existing conference participants are rediscovered by resync.
There are at most 200 tracked outgoing attempts/connected calls; remove old failed
rows to free space. A lost AMI connection disables call control until reconnection.
Missing terminal events are reconciled after the dial timeout plus ten seconds:
a fresh conference snapshot protects already-connected participants before any
remaining expired attempt is hung up.

## Local Asterisk test stand

`deploy/` contains a docker-compose stand with a real Asterisk: ConfBridge room **1000**
and two softphone endpoints, **1001** and **1002**.

```sh
docker compose -f deploy/docker-compose.yml up -d --build
cp deploy/.env.example deploy/.env
set -a; . ./deploy/.env; set +a
make build
.bin/westbridge bootstrap-admin admin  # first run only
.bin/westbridge
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
WB_ALLOWED_ORIGINS=localhost:5173 .bin/westbridge &
cd frontend && npm run dev
```

`WB_ALLOWED_ORIGINS` is needed because the Vite proxy forwards the dev server's own
`Origin` while rewriting `Host` to the backend, so the `/ws` handshake looks cross-origin.
Without it the REST calls still work and the socket is refused with a 403. Point the proxy
elsewhere with `WB_DEV_BACKEND`.

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
