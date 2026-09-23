# Westbridge

A web control panel for an **Asterisk ConfBridge** audio conference: invite
participants, control microphones, and manage personal phonebooks in one place.

The backend is written in Go, with a React and TypeScript frontend. The application
ships as a single executable with the frontend embedded. Users, sessions, and
contacts are stored in SQLite. Calls require a separate SIP client or phone.

## Features

- A shared conference with a live participant list.
- Multiple simultaneous outgoing calls, with cancellation, retry, and status
  indicators for busy, unanswered, and failed calls.
- **Mute / Unmute**, participant removal (**Kick**), and a speaking indicator.
- Personal phonebooks with contact creation, editing, deletion, selection, and
  group calling. Contact names appear in the owner's participant list.
- Username and password authentication, user and administrator roles, and a
  user management interface.
- Compact, independently scrollable lists and visible Asterisk connection status.

## Quick Start

Run Westbridge locally with Asterisk in Docker. Execute the commands from the
repository root using `bash` or `zsh`.

### Requirements

- **Go 1.27+**, as specified in `go.mod`.
- **Node.js 24+**, npm, and make to build the frontend.
- **Docker with Compose** for the local Asterisk stand.
- A SIP client to test calls.

### 1. Configure the environment and start Asterisk

```sh
# First run only; do not overwrite an existing deploy/.env.
cp deploy/.env.example deploy/.env
set -a
. ./deploy/.env
set +a

# Make the web panel accessible only from this computer.
export WB_LISTEN=127.0.0.1:8080

docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml ps
```

Wait for Asterisk to become `healthy`. The example environment includes AMI
credentials, room **1000**, and `WB_COOKIE_SECURE=false` for local HTTP.

### 2. Build the application and create an administrator

```sh
make build
.bin/westbridge bootstrap-admin admin
```

The command prompts for a password twice. It works only with an empty user
database; skip it on subsequent runs. Passwords must contain at least **3 characters**.

### 3. Open the panel

```sh
.bin/westbridge
```

Open **[http://127.0.0.1:8080](http://127.0.0.1:8080)** and sign in with the account
you created. Add other users through **Users**.

### 4. Connect a SIP client

| Setting | Value |
| --- | --- |
| SIP server | `127.0.0.1:5060` |
| Transport | UDP |
| First account | `1001` / `1001-secret` |
| Second account | `1002` / `1002-secret` |
| Conference number | `1000` |

Dial **1000** from the client, or register an account and call its number using
**Call** in the panel. SIP accounts and web accounts are independent.
These SIP passwords are intended only for the local test stand.

Press `Ctrl+C` to stop Westbridge. To stop Asterisk:

```sh
docker compose -f deploy/docker-compose.yml stop
```

On subsequent runs, load `deploy/.env`, set `WB_LISTEN`, start the container, and
run `.bin/westbridge`. Rebuild after changing the code.
For more stand details, see [deploy/README.md](deploy/README.md).

## Using the Conference

| Row | State | Actions |
| --- | --- | --- |
| Green | Participant connected to ConfBridge | Mute / Unmute, Kick |
| Yellow | Dialing or joining the conference | Cancel |
| Red | Busy, no answer, or an error | Retry, Remove |

You can dial the next number as soon as a call is submitted. **Cancel** hangs up
the channel in Asterisk; **Remove** dismisses a failed attempt. **Kick** requires
confirmation. A participant whose microphone is muted can still hear everyone else.

The icon after the phone number indicates sound activity. It turns off when the
participant is muted or the connection is lost. Background noise can also trigger
it. The test profile detects the end of speech after 2.5 seconds of silence.

Select contacts in **Phonebook** and press **Call selected**. Successfully submitted
calls are deselected; submission errors remain beside their contacts.
Phonebooks are private, while the conference and call controls are shared.

## Connecting Your Own Asterisk

Docker is optional if you already have Asterisk with ConfBridge and AMI.
Set `WB_AMI_ADDR`, `WB_AMI_USER`, `WB_AMI_SECRET`, `WB_ROOM`, and
`WB_ORIGINATE_CONTEXT` for your installation.

Configuration examples:

- [manager.conf](deploy/asterisk/manager.conf): AMI account and permissions:
  `read = system,call,reporting`, `write = system,call,originate,reporting`.
- [extensions.conf](deploy/asterisk/extensions.conf): conference entry and the
  outgoing call context. Configure your SIP endpoints or trunk in that context.
- [confbridge.conf](deploy/asterisk/confbridge.conf): conference profiles.
  Enable `talk_detection_events = yes` in the user profile for the speaking indicator.

The bundled stand assumes SIP clients run **on the same computer**. For clients
on other devices, set both external addresses in [pjsip.conf](deploy/asterisk/pjsip.conf)
(`external_signaling_address` and `external_media_address`) to the host's LAN IP,
use that address in the clients, and restart the container.

The RTP range in [rtp.conf](deploy/asterisk/rtp.conf), **10000–10100/UDP**, must match
Docker's published ports. The stand terminates calls after 60 seconds without
incoming RTP, or 300 seconds while on hold. This recovers abandoned connections;
normal hangup is handled through SIP BYE.

## Configuration

Settings are read from environment variables. Load the `.env` file into your shell
explicitly; see [deploy/.env.example](deploy/.env.example).

| Variable | Default | Purpose |
| --- | --- | --- |
| `WB_LISTEN` | `:8080` | HTTP listen address |
| `WB_DB_PATH` | `data/westbridge.db` | SQLite path, relative to the working directory |
| `WB_COOKIE_SECURE` | `true` | HTTPS-only cookies; use `false` for local HTTP |
| `WB_AMI_ADDR` | `127.0.0.1:5038` | AMI address |
| `WB_AMI_USER` | Required | AMI username |
| `WB_AMI_SECRET` | Required | AMI password |
| `WB_ROOM` | Required | ConfBridge conference number |
| `WB_ORIGINATE_CONTEXT` | Required | Outgoing call context |
| `WB_ORIGINATE_CALLERID` | `Westbridge <0000>` | Outgoing caller ID |
| `WB_ORIGINATE_TIMEOUT` | `30s` | Dial timeout |
| `WB_RESYNC_INTERVAL` | `30s` | Participant resynchronization interval |
| `WB_ALLOWED_ORIGINS` | Empty | Additional trusted Origin hosts for API and WebSocket, comma-separated |

## Users and Data Storage

Administrators use **Users** to create accounts, change roles and passwords, and
disable accounts. The last active administrator cannot be disabled or demoted.
Regular users can control the conference and manage their own phonebooks.

Authentication uses server-side sessions with opaque tokens in
**HttpOnly / SameSite=Strict cookies**. Passwords are hashed with Argon2id, and
session tokens with SHA-256. Sessions last 12 hours and survive server restarts.
Signing out revokes the current session; an administrator's account change revokes
all sessions for that account.

SQLite stores users, sessions, and contacts. Keep the database in a persistent
directory and include it in backups. Administrator bootstrapping and the server
must use the same `WB_DB_PATH`.

Outside the local stand, serve the application over HTTPS with WebSocket proxy
support, keep `WB_COOKIE_SECURE=true`, replace the test passwords, and restrict
AMI access. No external database server is required.

## Development

```sh
make build                    # Build frontend and Go binary in .bin/westbridge
make test                     # Run Go tests
make lint                     # Run golangci-lint and TypeScript type checking
make                          # Lint, test, and build
npm --prefix frontend test    # Run frontend tests
npm --prefix frontend run lint
```

`make build` installs frontend dependencies if `node_modules` is missing. The
frontend is built into `internal/web/assets/dist` and embedded in the Go binary.
Node.js and frontend source files are not required to run the resulting binary.

For frontend development, start the backend with its environment loaded:

```sh
WB_ALLOWED_ORIGINS=localhost:5173,127.0.0.1:5173 .bin/westbridge
```

In a second terminal:

```sh
cd frontend
npm ci
npm run dev
```

Vite proxies `/api` and `/ws` to `http://127.0.0.1:8080`.
Set `WB_DEV_BACKEND` when starting Vite to use a different backend address.

## Architecture

```text
Browser ── HTTP/JSON ──► Go backend ── AMI/TCP ──► Asterisk ConfBridge
        ◄─ WebSocket ──            ◄─ events ───
                             │
                           SQLite
```

The backend maintains one AMI connection for commands and events. The browser
sends commands over REST and receives full conference snapshots over WebSocket.
On reconnect and at regular intervals, `ConfbridgeList` restores the participant
list. Speaking activity arrives separately through `ConfbridgeTalking`.

| Directory | Contents |
| --- | --- |
| `cmd/westbridge/` | Startup, configuration, administrator bootstrap |
| `internal/ami/` | AMI client and protocol |
| `internal/conference/` | Participants, calls, mute, and speaking activity |
| `internal/auth/` | SQLite, users, sessions, and contacts |
| `internal/hub/` | WebSocket snapshot broadcasting |
| `internal/web/` | HTTP API and embedded frontend |
| `frontend/` | React, TypeScript, and Vite |
| `deploy/` | Local Asterisk stand |

## HTTP API

All routes except login require a session cookie. Mutating requests require
`Content-Type: application/json` and are subject to Origin validation.
Errors are returned as `{"error":"..."}`.

| Method | Path | Purpose / request body |
| --- | --- | --- |
| POST | `/api/auth/login` | `{"login":"...","password":"..."}` |
| GET | `/api/auth/me` | Current user |
| POST | `/api/auth/logout` | Sign out |
| GET / POST | `/api/users` | List / create users, administrator only |
| PATCH | `/api/users/{id}` | Update `role`, `enabled`, or `password`, administrator only |
| GET / POST | `/api/contacts` | Personal phonebook / create: `{"name":"Alice","number":"1002"}` |
| PUT / DELETE | `/api/contacts/{id}` | Replace name and number / delete an owned contact |
| GET | `/api/conference` | Current conference state |
| POST | `/api/conference/participants` | Invite: `{"number":"1002"}` |
| DELETE | `/api/conference/participants/{uniqueid}` | Kick a participant |
| PUT | `/api/conference/participants/{uniqueid}/mute` | `{"muted":true}`; use `false` to unmute |
| DELETE | `/api/conference/calls/{id}` | Cancel dialing / dismiss a failed attempt |
| POST | `/api/conference/calls/{id}/retry` | Retry a call |
| GET | `/ws` | WebSocket snapshots: `type`, `room`, `asteriskConnected`, `participants`, `calls` |

See [frontend/src/types.ts](frontend/src/types.ts) for the wire types.

## Limitations and Troubleshooting

- Each Westbridge instance controls one shared conference.
- The test Asterisk profile sets **`max_members = 20`**. Increase this limit for
  40–50 participants; the compact UI does not change the conference capacity.
- Outgoing attempt rows are stored in memory and reset on a Westbridge restart.
  Connected participants are rediscovered from Asterisk. There is no call history.
- Up to 200 outgoing attempts and their connected calls can be tracked;
  dismiss old failed attempts with **Remove** to free space.
- After AMI reconnects, the speaking indicator waits for a new activity event.

| Symptom | What to check |
| --- | --- |
| No connection to Asterisk | Container status, `WB_AMI_*`, port 5038, and allowed addresses in `manager.conf` |
| `ConfbridgeList: Permission denied` | The AMI account needs the `write = reporting` permission |
| No audio or participants remain after hangup | External SIP/RTP addresses, `local_net`, RTP port forwarding, and SIP BYE delivery |
| Login cookie is not saved locally | Set `WB_COOKIE_SECURE=false` for HTTP |
| No frontend after startup | Run `make build`, not just `go build` |
| Docker CLI is unavailable | Start Docker Desktop and make sure Docker and Compose are available in `PATH` |

Useful commands:

```sh
docker compose -f deploy/docker-compose.yml logs --tail=100 asterisk
docker compose -f deploy/docker-compose.yml exec asterisk asterisk -rx 'confbridge list 1000'
docker compose -f deploy/docker-compose.yml exec asterisk asterisk -rx 'core show channels concise'
```
