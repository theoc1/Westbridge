# Plan: Westbridge — Web Control for Asterisk ConfBridge Conferences

## Overview

A small web application to control a single Asterisk ConfBridge audio conference:
see the participant list in real time, kick a participant, and add a participant
by dialing a phone number.

Backend is Go (single binary, embedded frontend assets), frontend is React + Vite + TypeScript.
The backend holds exactly **one** persistent AMI TCP connection to Asterisk and uses it for
both event streaming and commands. The WebSocket lives between the browser and our backend,
not between the backend and Asterisk.

## Original Prompt (verbatim)

Kept word-for-word so the same request can be replayed against other models.

```
Нужно написать план для приложения

Формат плана - https://ralphex.com/ посмотри документацию и примеры планов

Приложение для того, чтобы контролировать аудио-конференции IP PBX Asterisk через web
Для MVP достаточно самого простого функционала. Список участников в реальном времени, убрать участника, добавить участника по номеру телефона.
Интерфейсы со стороны Asterisk - Websocket для списка участников в реальном времени + AMI для команд. Проверить возможность AMI поверх Websocket, чтобы не держать лишние открытые интефейсы.
Backend - golang, Frontend - React+Vite

Задай все необходимые вопросы, чтобы прояснить детали, которые я не указал.

Сохрани изначальный промпт в плане слово в слово, чтобы я мог его повторить на других моделях.
```

## Research Findings

#### AMI over WebSocket does not exist

Verified against `asterisk/asterisk` master:

- `main/manager.c` contains **zero** references to websocket.
- There is no `res_ami_websocket` module in the tree. The only websocket modules are
  `res_http_websocket`, `res_websocket_client`, `res_pjsip_transport_websocket`, and
  `chan_websocket` (media).
- AMI transports are therefore only: raw TCP/TLS on port 5038, and AMI-over-HTTP
  (`/manager`, `/rawman`, `/mxml`, plus digest variants `/amanager`, `/arawman`, `/amxml`)
  when `manager.conf` has `webenabled=yes`. AMI-over-HTTP delivers events via long-poll
  (`/rawman?action=waitevent`), not via WebSocket.
- WebSocket in Asterisk exists only for ARI events (`/ari/events`), WebRTC signalling,
  and `chan_websocket` media.

#### ARI cannot see ConfBridge conferences

ARI's `/bridges` only exposes bridges created by ARI/Stasis. A conference started from the
dialplan with `ConfBridge()` is invisible to ARI. Participant lists and control for ConfBridge
are available **only** through AMI.

#### Decision

One persistent AMI TCP connection (port 5038) carries everything:

- events `ConfbridgeJoin` / `ConfbridgeLeave` / `ConfbridgeStart` / `ConfbridgeEnd` for the live roster;
- actions `ConfbridgeList` (initial + resync snapshot), `ConfbridgeKick`, `Originate` for commands.

This exposes exactly one Asterisk interface — fewer than the original two-interface idea.

#### Confirmed decisions

| Question | Decision |
| --- | --- |
| Asterisk transport | Single AMI TCP connection on 5038 |
| Conference engine | ConfBridge, already configured in the dialplan |
| Rooms | One room, its number comes from config |
| Web auth | None (trusted network / VPN) |
| Adding a participant | `Originate` with `Channel: Local/<number>@<context>`, `Application: ConfBridge` |
| Test environment | docker-compose with a real Asterisk |
| Frontend delivery | Vite build embedded into the Go binary via `embed.FS` |
| MVP scope | Strictly three features: list, kick, add |

## Architecture

```
browser  --HTTP/JSON--> Go backend --AMI TCP 5038--> Asterisk
        <--WebSocket---            (single connection)
```

- **WebSocket is one-way (server -> browser)** and carries state only. Commands go over plain
  REST. This keeps error handling trivial and avoids inventing a request/response protocol on
  top of the socket.
- **Every state change broadcasts a full snapshot.** A conference holds tens of participants at
  most, so a full snapshot is a few hundred bytes and removes an entire class of desync bugs.
- **The roster is keyed by channel `Uniqueid`**, which is stable. The channel *name* is also kept
  because `ConfbridgeKick` addresses participants by channel name.
- **Recovery:** on every AMI (re)connect, and on a periodic 30s timer, the backend re-runs
  `ConfbridgeList` and rebuilds the roster from scratch. Missed events cannot accumulate into drift.
- **AMI link state is surfaced in the UI.** When the AMI connection is down, the frontend shows the
  roster as stale rather than silently displaying frozen data.

#### Target layout

```
cmd/westbridge/main.go          entry point, config, wiring, graceful shutdown
internal/ami/                   AMI protocol codec + client (connection, login, actions, events)
internal/conference/            roster state + service (list / kick / invite)
internal/hub/                   browser WebSocket pub/sub
internal/web/                   HTTP handlers, WS endpoint, embedded assets
internal/web/assets/dist/       Vite build output (embedded)
frontend/                       React + Vite + TypeScript sources
deploy/                         docker-compose stand + Asterisk configs
```

#### REST + WebSocket contract

```
GET    /api/conference                        -> 200 {"room":"1000","asteriskConnected":true,"participants":[...]}
POST   /api/conference/participants           body {"number":"1002"} -> 202 {"actionId":"..."}
DELETE /api/conference/participants/{uniqueid}-> 204
GET    /ws                                    -> WebSocket, server->client only

participant := {
  "uniqueid": "1756...", "channel": "PJSIP/1001-0000000a",
  "callerIdNum": "1001", "callerIdName": "Alice",
  "admin": false, "muted": false, "joinedAt": "2026-09-01T10:00:00Z"
}

ws message := {"type":"snapshot","room":"1000","asteriskConnected":true,"participants":[...]}
```

#### Configuration (environment variables)

| Variable | Default | Meaning |
| --- | --- | --- |
| `WB_LISTEN` | `:8080` | HTTP listen address |
| `WB_AMI_ADDR` | `127.0.0.1:5038` | Asterisk AMI address |
| `WB_AMI_USER` | — | AMI username (required) |
| `WB_AMI_SECRET` | — | AMI secret (required) |
| `WB_ROOM` | — | ConfBridge room number (required) |
| `WB_ORIGINATE_CONTEXT` | — | Dialplan context for outbound calls (required) |
| `WB_ORIGINATE_CALLERID` | `Westbridge <0000>` | Caller ID for originated calls |
| `WB_ORIGINATE_TIMEOUT` | `30s` | Dial timeout, sent to AMI as milliseconds |
| `WB_RESYNC_INTERVAL` | `30s` | Periodic full-roster resync |

## Success Criteria

- Opening the app shows the live ConfBridge roster; joins and leaves appear within ~1s without a page reload.
- Kicking a participant from the UI drops their call and removes them from every connected browser.
- Adding a number places a call; when answered, the callee appears in the conference and in the roster.
- Killing and restarting Asterisk shows a disconnected state in the UI, then a correct roster once the AMI link recovers, with no stale entries.
- `make lint`, `make test`, and `make build` all pass; Go packages `ami` and `conference` are covered by unit tests against a fake AMI server.
- The whole app ships as one Go binary with the frontend embedded.

## Constraints and Gotchas

- **`Originate` must use `Async: true`.** A synchronous Originate blocks the AMI connection for the
  entire dial timeout, and the app has only one connection. With `Async: true` the action returns
  immediately and the outcome arrives later as an `OriginateResponse` event carrying the same `ActionID`.
- **`Originate` `Timeout` is in milliseconds**, not seconds.
- **List-style actions are asynchronous too.** `ConfbridgeList` returns a `Response: Success`
  acknowledgement, then N `ConfbridgeList` events, then a `ConfbridgeListComplete` event — all
  sharing the request's `ActionID`. The client must accumulate until the terminator arrives.
- **`go:embed` needs a non-empty directory.** `internal/web/assets/dist` must contain a committed
  `.gitkeep`, and the directive must be `//go:embed all:dist` (the `all:` prefix is what makes
  dotfiles match), otherwise `go build` fails on a fresh clone before the frontend is built.
- **RTP through Docker on macOS is unreliable.** Audio may not flow on the local stand. That is
  acceptable: this app cares about signalling and roster state, not media. Do not spend time
  debugging one-way audio on the test stand.
- **Local toolchain needs upgrading before anything else works.** The machine currently has
  Go 1.15 (needs 1.22+ for `net/http` method patterns and `embed`), no `golangci-lint`, and the
  `docker` CLI is not on `PATH` even though Docker.app is installed. Node is v20.12.1, which is
  below Vite 7's floor of 20.19 — pin Vite 6 unless Node is upgraded.

## Validation Commands

- `make lint`
- `make test`
- `make build`

### Task 1: Repository scaffold and toolchain

Establish a repo where all three validation commands pass before any feature code exists.
This task is complete only when `make lint && make test && make build` is green on an empty app.

- [x] Verify Go >= 1.22 is on `PATH` (`go version`); if not, install a current Go toolchain and document the requirement in README (found Go 1.27.0, requirement documented in README)
- [x] Verify `docker` and `docker compose` are on `PATH`; if Docker.app is installed but the CLI is missing, document the fix in README (CLI missing; symlink fix documented in README)
- [x] Create `go.mod` (module `github.com/dmalkin/westbridge`, adjust to the real path) targeting the installed toolchain
- [x] Create `cmd/westbridge/main.go` with a stub `main` that parses config and exits, so `go build ./...` succeeds
- [x] Scaffold `frontend/` with `npm create vite@latest -- --template react-ts`; pin Vite 6 if Node is below 20.19 (Node 26.8, so Vite 8 as scaffolded — no pin needed)
- [x] Set `build.outDir` to `../internal/web/assets/dist` and `emptyOutDir: true` in `vite.config.ts`
- [x] Create `internal/web/assets/dist/.gitkeep` and commit it; add `internal/web/assets/dist/*` (except `.gitkeep`) to `.gitignore`
- [x] Add `.golangci.yml` (enable `errcheck`, `govet`, `staticcheck`, `revive`, `gosec`) and install `golangci-lint` into `.bin/`
- [x] Add `Makefile` with `build` (frontend then `go build -o .bin/westbridge ./cmd/westbridge`), `test` (`go test ./...`), `lint` (`golangci-lint run` + `tsc --noEmit`), and `frontend` (`npm ci` if `node_modules` is missing, then `npm run build`)
- [x] Add `.gitignore` for `.bin/`, `node_modules/`, and build output

### Task 2: AMI protocol codec

The AMI wire format is plain text: `Key: Value\r\n` lines, packets terminated by a blank line.
Implement it as a standalone, fully tested layer with no networking so the client on top stays thin.

- [x] Create `internal/ami/message.go` with a `Message` type preserving field order and supporting repeated keys (`Variable:` may appear many times)
- [x] Implement `Message.Get(key)` with case-insensitive lookup — Asterisk's own casing is not stable across versions
- [x] Implement `Message.WriteTo(w)` emitting `Key: Value\r\n` lines plus the trailing `\r\n`
- [x] Implement a `Decoder` over `bufio.Reader` reading one packet per call, with a size cap to reject an unbounded packet
- [x] Add helpers `IsResponse()`, `IsEvent()`, `EventName()`, `ActionID()`
- [x] Handle the AMI banner line (`Asterisk Call Manager/x.y.z`) as a distinct first read, not as a packet
- [x] Write `internal/ami/message_test.go` covering multi-value keys, CRLF vs LF tolerance, the banner, oversized packets, and round-tripping

### Task 3: AMI client with reconnect

A single connection multiplexing request/response, list-style actions, and an event stream.
One reader goroutine owns the socket; everything else talks to it through channels.

- [x] Create `internal/ami/client.go`: `Dial`, banner read, `Login` action, and a `Run(ctx)` loop owning the single reader goroutine
- [x] Generate unique `ActionID`s and correlate responses through a mutex-guarded `map[string]chan *Message`
- [x] Implement `Action(ctx, msg) (*Message, error)` for single-response actions with a context deadline
- [x] Implement `ActionList(ctx, msg, completeEvent string) ([]*Message, error)` accumulating events until the terminator event with the matching `ActionID` arrives
- [x] Expose unsolicited events on a channel; drop-with-warning on a slow consumer rather than blocking the reader
- [x] Implement auto-reconnect with exponential backoff (1s -> 30s cap) and expose a `Connected() bool` plus a state-change callback
- [x] Fail every in-flight action with a clear error when the connection drops, so no caller hangs
- [x] Build `internal/ami/amitest` — a fake AMI server over `net.Listener` that speaks the banner, accepts `Login`, and replays scripted responses and events
- [x] Write `internal/ami/client_test.go` against the fake: successful login, bad credentials, single action, list action, event delivery, mid-action disconnect, reconnect

### Task 4: Conference roster and service

The domain layer: what the conference currently looks like and the three operations on it.
No HTTP and no WebSocket in this package.

- [x] Create `internal/conference/participant.go` with the `Participant` struct (uniqueid, channel, callerIdNum, callerIdName, admin, muted, joinedAt)
- [x] Create `internal/conference/roster.go` — a mutex-guarded map keyed by `Uniqueid`, with `Snapshot()` returning a sorted copy (by `joinedAt`, then uniqueid) for stable UI ordering
- [x] Create `internal/conference/service.go` with `List(ctx)` issuing `ConfbridgeList` for the configured room and replacing the roster wholesale
- [x] Implement `Kick(ctx, uniqueid)` — resolve the channel name from the roster, then send `ConfbridgeKick` with `Conference` and `Channel`; return a clear error for an unknown uniqueid
- [x] Implement `Invite(ctx, number)` — `Originate` with `Channel: Local/<number>@<context>`, `Application: ConfBridge`, `Data: <room>`, `CallerID`, `Timeout` in milliseconds, and `Async: true`
- [x] Validate and normalize the dialed number (digits and a leading `+` only) before it reaches the dialplan, and reject anything else
- [x] Handle events `ConfbridgeJoin`, `ConfbridgeLeave`, `ConfbridgeStart`, `ConfbridgeEnd`, filtering on the configured `Conference` field and ignoring other rooms
- [x] Log `OriginateResponse` outcomes matched by `ActionID` so failed invites are diagnosable
- [x] Trigger a full `List()` resync on every AMI reconnect and on a `WB_RESYNC_INTERVAL` ticker
- [x] Notify subscribers with a full snapshot on every roster change and on every AMI state change
- [x] Write `internal/conference/*_test.go` against the fake AMI server: initial list, join, leave, kick of an unknown participant, invite argument shape, resync after reconnect

### Task 5: HTTP server, WebSocket hub, and wiring

- [x] Create `internal/hub/hub.go` — subscribe/unsubscribe/broadcast over `chan []byte`, per-client buffered channel, disconnect a client that falls behind instead of blocking the broadcaster
- [x] Write `internal/hub/hub_test.go` covering concurrent subscribe/broadcast/unsubscribe and slow-client eviction
- [x] Create `internal/web/server.go` using the stdlib `http.ServeMux` with Go 1.22 method patterns; add `github.com/coder/websocket` as the WS dependency
- [x] Implement `GET /api/conference`, `POST /api/conference/participants`, `DELETE /api/conference/participants/{uniqueid}` returning JSON errors as `{"error":"..."}`
- [x] Implement `GET /ws`: send the current snapshot immediately on connect, then stream subsequent snapshots; run a ping/pong keepalive and drop dead sockets
- [x] Create `internal/web/assets/assets.go` with `//go:embed all:dist`, serving `index.html` as the SPA fallback for unknown non-API paths
- [x] Add request logging and a panic-recovery middleware
- [x] Wire everything in `cmd/westbridge/main.go`: env config with validation of required variables, `log/slog` setup, AMI client, conference service, hub, HTTP server, and graceful shutdown on SIGINT/SIGTERM
- [x] Make the backend start and stay up when Asterisk is unreachable, reporting `asteriskConnected: false` rather than exiting
- [x] Write `internal/web/server_test.go` using `httptest` for the REST endpoints and one WebSocket snapshot test

### Task 6: React frontend

Single screen, no router, no UI library. Plain CSS — the surface is one list and one form.

- [ ] Create `frontend/src/types.ts` mirroring the participant and snapshot JSON shapes
- [ ] Create `frontend/src/api.ts` with typed `getConference`, `addParticipant`, `kickParticipant` helpers that surface backend error messages
- [ ] Create `frontend/src/useConference.ts` — a hook owning the WebSocket, with exponential-backoff reconnect, cleanup on unmount, and a REST fetch as the initial fallback
- [ ] Create `frontend/src/components/ParticipantList.tsx` showing caller ID, number, channel, and time in conference, with a per-row kick button
- [ ] Add a confirmation step before kicking, and disable the row's button while the request is in flight
- [ ] Create `frontend/src/components/AddParticipantForm.tsx` with client-side number validation, a pending state, and inline error display
- [ ] Create `frontend/src/components/StatusBar.tsx` showing room number, participant count, and the Asterisk connection state
- [ ] Grey out the roster and show an explicit warning banner when `asteriskConnected` is false
- [ ] Render an empty state when the conference has no participants
- [ ] Add `server.proxy` in `vite.config.ts` for `/api` and `/ws` (with `ws: true`) so `npm run dev` works against the Go backend
- [ ] Write `frontend/src/styles.css` — readable defaults, works down to a phone-width viewport

### Task 7: docker-compose test stand

A local Asterisk that can be joined by a softphone, so the full flow is verifiable by hand.

- [ ] Create `deploy/docker-compose.yml` with an Asterisk service (pin an explicit image tag; if no suitable image exists, add `deploy/asterisk/Dockerfile` building from a distro package)
- [ ] Expose 5038/tcp (AMI), 5060/udp (SIP), and 10000-10100/udp (RTP)
- [ ] Write `deploy/asterisk/manager.conf` with a `westbridge` user, `permit` for the container network, `read = system,call,reporting`, `write = system,call,originate`; verify these permission classes actually allow `ConfbridgeList`, `ConfbridgeKick`, and `Originate` and widen them only if a command is rejected
- [ ] Write `deploy/asterisk/http.conf` — leave it disabled, the app needs only AMI TCP
- [ ] Write `deploy/asterisk/pjsip.conf` with a UDP transport and two softphone endpoints, `1001` and `1002`
- [ ] Write `deploy/asterisk/confbridge.conf` with a `default_bridge` and `default_user` profile
- [ ] Write `deploy/asterisk/extensions.conf`: context `internal` with `exten => 1000` running `ConfBridge(1000)`, and context `conference-out` with `exten => _X.` running `Dial(PJSIP/${EXTEN},30)`
- [ ] Add `deploy/.env.example` with matching `WB_*` values (`WB_ROOM=1000`, `WB_ORIGINATE_CONTEXT=conference-out`)
- [ ] Document in `deploy/README.md` how to register a softphone against endpoint 1001 and dial 1000

### Task 8: End-to-end verification and documentation

- [ ] Bring up the stand with `docker compose -f deploy/docker-compose.yml up -d` and confirm AMI login succeeds in the backend logs
- [ ] Register a softphone as 1001, dial 1000, and verify the participant appears in the UI within ~1s
- [ ] Open two browser tabs and verify both receive the same updates
- [ ] Add participant `1002` from the UI, answer on a second softphone, and verify the callee joins the conference and the roster
- [ ] Kick a participant from the UI and verify the call drops and every tab updates
- [ ] Restart the Asterisk container and verify the UI shows the disconnected state, then recovers with a correct roster and no stale entries
- [ ] Verify the built binary serves the embedded frontend with no `frontend/` directory present at runtime
- [ ] Write `README.md`: what the app does, why AMI over WebSocket is not possible, configuration table, build instructions, and how to run the test stand
- [ ] Run `make lint`, `make test`, and `make build` one final time and confirm all three are clean
