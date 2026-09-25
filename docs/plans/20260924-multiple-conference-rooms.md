# Plan: Multiple Conference Rooms and Administrator-Managed Access

## Overview

Replace the single configured conference with persistent rooms managed from the
administrator panel. Administrators create and delete rooms and assign users.
Users can see and control only their assigned rooms. Each room has independent
participants, outgoing attempts, mute state and speaking indicators.

Creating a room provisions both its Westbridge record and its Asterisk admission
configuration. Keep one AMI connection and the existing ConfBridge engine. Preserve
personal phonebooks and existing call behaviour. External SIP registrations and
PSTN routing are a separate future feature, not part of this implementation.

## Original Prompt (verbatim)

```
посмотри в docs/plans/ формат плана. напиши план сделать несколько комнат конференций. создавать и удалять и разрешать их пользователям можно только из админ-панели. конференции должны создаваться в westbridge и в asterisk
```

## Research Findings

### Existing implementation

- `conference.Service`, `ami.CallController`, the HTTP API and the WebSocket hub
  currently target one room supplied by `WB_ROOM`.
- Multiple services must not independently read `ami.Client.Events()`: readers
  would compete for events instead of each receiving its own room's events.
- Outgoing events do not always contain a room; attempts must be routed by their
  stable application ID as well as by the conference identifier.
- `database/` now owns shared SQLite initialization; `auth/` owns accounts and
  sessions, `phonebook/` owns personal contacts, and `telephony/` owns shared types.
- The stand has a hard-coded extension `1000`, endpoints `1001` and `1002`, and
  common ConfBridge profiles. These extension numbers must not collide.

### Asterisk provisioning

ConfBridge takes a conference identifier when a channel enters the application.
Its profiles configure behaviour, rather than providing a persistent catalogue of
empty rooms. See [ConfBridge](https://docs.asterisk.org/Asterisk_21_Documentation/API_Documentation/Dialplan_Applications/ConfBridge/)
and [profiles](https://docs.asterisk.org/Configuration/Applications/Conferencing-Applications/ConfBridge/ConfBridge-Configuration/).

The proposed implementation maintains a persistent managed-room registry in
AstDB. AMI supports [DBPut](https://docs.asterisk.org/Latest_API/API_Documentation/AMI_Actions/DBPut/)
and [DBDel](https://docs.asterisk.org/Latest_API/API_Documentation/AMI_Actions/DBDel/);
the dialplan can check entries using
[DB_EXISTS](https://docs.asterisk.org/Latest_API/API_Documentation/Dialplan_Functions/DB_EXISTS/).

This is our provisioning design, not a built-in ConfBridge create-room API.
An empty provisioned room is valid even when absent from the active conference
list. Verify activation on first entry and disappearance after the last departure
against the stand's actual Asterisk version. No dummy participant should be needed.

## Decisions and Scope

These defaults resolve behaviour not specified in the request; make them visible
in the UI and documentation.

| Question | Planned behaviour |
| --- | --- |
| Management | Only administrators create/delete rooms and grant/revoke access, through the existing admin panel and protected API |
| Administrator access | Administrators can view and control every room without individual grants |
| Regular user access | Explicit per-room assignment; a new user initially has no rooms |
| Meaning of assignment | View participants and use all existing call controls in that room |
| Room identity | Immutable application ID, unique numeric dial-in extension, display name |
| Number allocation | Admin supplies a number in a deployment-reserved conference range; stand default 7000–7999; legacy 1000 is a migration exception |
| Room editing | No renumbering in this iteration; no per-room audio profiles |
| Phonebook | Remains personal and shared across that user's accessible rooms |
| Deletion | Reject known busy rooms with 409; no implicit force-hangup |
| Provisioning failures | Persist pending intent and expose sync errors to administrators; never report ready prematurely |
| Asterisk topology | One managed Asterisk and one Westbridge backend process |
| SIP/PSTN | No trunks, registrations, provider credentials or outbound route management yet |

Web accounts are not SIP endpoint identities. Room grants protect the web API and
WebSocket; they do not identify a person dialling directly from a softphone.
Direct dial-in follows the deployment's trusted SIP contexts. Per-user SIP admission,
PINs and external inbound access are out of scope and must not be implied by the UI.

## Architecture

```text
Admin panel -> room catalogue/access service -> SQLite desired state
                                      |
                               room provisioner
                                      |
                              AMI adapter -> AstDB
                                              |
                                     managed admission dialplan
                                              |
                                          ConfBridge

Browser -> room authorization -> conference manager -> per-room runtime
                                      ^                    |
                              single event dispatcher      v
                                    AMI events       per-room snapshots
```

### Boundaries

- `internal/rooms/`: room definitions, membership, lifecycle policy and persistence.
- `internal/database/`: versioned schema changes and database lifetime.
- `internal/auth/`: identity, role and session validation; no room tables or policy.
- `internal/conference/`: manager and independent live room state; retain the pure
  call model, rather than creating a second state machine for every room.
- `internal/ami/`: AstDB operations, dialplan contract, command construction and
  translation/correlation of room and call events. Expose semantic operations
  such as ensure-room, disable-admission and remove-room to their consumers.
- `internal/web/`: enforce authentication, room authorization and HTTP/WS contracts.
- `internal/phonebook/`: unchanged ownership; batch invitations receive an explicit
  target room and are checked by the conference API.

Keep interfaces small and defined near consumers. Shared contract types belong in
`telephony/` only when needed to avoid circular dependencies. Do not put AstDB keys,
AMI messages, SQL or provider-specific errors into the call model.

### Persistence and upgrade

Add versioned, transactional SQLite migrations. Adopt the existing unversioned
schema without recreating user/session/contact tables.

- `rooms`: stable ID, unique dial-in number, name, desired state, provisioning status,
  diagnostic error and generation. Retain deletion tombstones until
  Asterisk cleanup is verified; do not reuse a number while cleanup is pending.
- `room_users`: room ID and user ID with a composite unique key and foreign keys.
- Schema version metadata; no generic job queue or speculative SIP tables.

On the first upgrade only, import `WB_ROOM` as the legacy room and grant existing
users access to preserve current use. Preserve its existing ConfBridge identifier
so a backend upgrade does not orphan live participants. Fresh installations begin
with no rooms. Later starts must not recreate a deleted legacy room or re-grant
revoked access. Make this distinction explicit in the migration tests.

### Asterisk registration and reconciliation

Use a dedicated AstDB family owned exclusively by this Westbridge installation.
Map the dial-in number to an immutable bridge identifier; use a separate enabled
marker if needed. Install a common admission context once. Both direct dial-in and
Westbridge-originated calls must pass through that gate before entering ConfBridge;
the existing `Application: ConfBridge` shortcut must not bypass deleted-room checks.
Keep outgoing call identity and correlation intact when changing the Originate path.

1. Commit the room and its desired state in SQLite.
2. Serialize provisioning per room, apply the matching generation in Asterisk,
   and read back the owned registry entry before declaring it ready.
3. On timeout, preserve an unknown outcome and retry idempotently. No SQL
   transaction remains open while waiting on AMI.
4. Reconcile pending records, tombstones and owned registry drift on startup,
   AMI reconnect and periodically. Never alter unrelated AstDB families or rooms.
5. A room accepts new web invitations only when it is ready and AMI is available.
   Readiness also requires the managed dialplan contract to be installed/verified;
   an AstDB write alone does not prove the room is usable.

Deletion first checks the live roster and outgoing attempts. If busy, return 409
and leave the room intact. Otherwise persist deleting intent, reject new web
operations, disable Asterisk admission, and verify no active participants or
in-flight attempts remain before removing registration and memberships. A join
racing with the precheck must keep deletion pending until that participant leaves;
do not silently drop the room record or force-disconnect the caller. Admins retain
visibility and termination controls for a deleting room. Test this admission race,
including calls admitted by the dialplan just before the gate closes. Preserve
pending deletion across disconnects and process restarts.

Use stable bridge IDs and retain tombstones so late events cannot affect a new
room with a recycled extension. Do not equate a missing active ConfBridge with a
missing provisioned room.

### Event and command isolation

One dispatcher consumes the AMI stream. Route conference events by bridge ID and
outgoing outcomes by attempt ID. Register attempt-to-room ownership before sending
Originate. Keep snapshot sequence boundaries and speaking-reset boundaries per
room, and discard unknown/retired events. Reconnect resynchronizes every managed
room with bounded work; one slow room must not starve events for the others.

Each room has its own roster, attempts and snapshot subscribers. Resolve participant
and attempt IDs within the requested room, never globally. Concurrent calls to the
same phone number in different rooms must remain independent. Switching the viewed
room never cancels calls in the previous room.

### HTTP and WebSocket contract

```text
GET    /api/rooms                                  accessible rooms
GET    /api/rooms/{roomID}/conference               room snapshot
POST   /api/rooms/{roomID}/participants             invite
DELETE /api/rooms/{roomID}/participants/{uniqueID}  kick
PUT    /api/rooms/{roomID}/participants/{uniqueID}/mute
DELETE /api/rooms/{roomID}/calls/{callID}           cancel/remove
POST   /api/rooms/{roomID}/calls/{callID}/retry
GET    /ws?roomId={roomID}                         one authorized room

GET    /api/admin/rooms                           all rooms and sync status
POST   /api/admin/rooms                           create: name, number
DELETE /api/admin/rooms/{roomID}                   request deletion
GET    /api/admin/rooms/{roomID}/users             current grants
PUT    /api/admin/rooms/{roomID}/users             replace granted user IDs
```

Create/delete return 202 while synchronization is pending; the admin list exposes
progress and errors. Unknown or inaccessible rooms return 404 to regular users;
non-admin management attempts return 403. Invalid input returns 400, duplicates or
busy deletion return 409, unavailable Asterisk returns 503 for live commands.

Snapshots include immutable room ID plus display name and number. Authorize every
REST operation and WebSocket subscription. Recheck access before each WS send and
on the existing periodic session check; revocation must stop future snapshots and
close the socket. Serialize authorization changes with command admission: commands
already accepted may finish, but later commands must fail. Revoking web access does
not itself hang up telephone participants.

Remove old unscoped conference routes as part of the same frontend/backend release;
never silently map them to an arbitrary room. Keep existing auth/user/phonebook APIs.
Extend admin middleware explicitly: current user-management path checks alone are
not sufficient for `/api/admin/rooms`.

## Success Criteria

- Admin creates two rooms; both persist in SQLite and are independently usable
  through the managed Asterisk dialplan after provisioning finishes.
- A regular user cannot create/delete rooms or change grants by any HTTP request.
- Users assigned different rooms cannot read or operate each other's participants,
  attempts or WebSocket streams, including forged cross-room IDs.
- Switching rooms preserves live calls and never shows another room's stale snapshot.
- Existing invite, cancel, retry, kick, mute and speaking detection work per room.
- Removing access closes an existing room socket and blocks subsequent commands.
- Empty provisioned rooms remain visible; deletion removes Asterisk admission and
  Westbridge access without silently interrupting a busy conference.
- Backend/Asterisk restart and partial provisioning failure recover consistently.
- Existing users, sessions and contacts survive upgrade; legacy access migrates once.
- All validation commands pass; real Asterisk checks verify the admission contract.

## Constraints and Gotchas

- SQLite and AstDB have no shared transaction; pending state and reconciliation are
  required. An AMI acknowledgement is not proof of complete provisioning.
- Do not spawn one AMI connection or competing event reader per room.
- AMI room commands and call identity translation stay in the adapter.
- Reserve a non-overlapping extension range at deployment; checking only the rooms
  table cannot detect collisions with unrelated Asterisk dialplan routes.
- Preserve the configured participant limit (currently 20) unless explicitly changed;
  supporting more rooms does not imply increasing capacity per room.
- Verify required AMI privileges on the installed version; add only the privileges
  needed for registry read/write and existing conference controls, not blanket `all`.
- Preserve or restore AstDB after container recreation; persist its data volume and
  test reconciliation when the registry starts empty.
- Web grants do not enforce SIP caller identity. Do not expose managed dial-in
  contexts to untrusted trunks as part of this iteration.

## Validation Commands

- `make lint`
- `make test`
- `go test -race ./...`
- `make build`
- `cd frontend && npm run lint`

### Task 1: Verify the Asterisk admission contract

- [x] Verify the stand's actual version, required modules and AMI registry permissions
- [x] Prototype the managed AstDB family and common dial-in/admission context
- [x] Verify first join, empty room, independent bridges, and unknown/deleted room rejection
- [x] Verify outgoing calls pass the same gate while retaining stable attempt identities
- [x] Establish how admission-in-progress is observed during deletion; test a join racing with gate closure before accepting the deletion implementation
- [x] Record the verified contract and deployment requirements without modifying unrelated dialplan routes

### Task 2: Add persistent rooms and access grants

- [x] Introduce versioned migrations in `database/` with safe adoption of the existing schema
- [x] Add `rooms/` types, validation, store, membership and lifecycle operations
- [x] Implement unique room numbers, reserved-range validation and retained deletion tombstones
- [x] Implement one-time legacy room import/access grants and empty fresh-install behaviour
- [x] Test upgrade/reopen, duplicates, failed migration rollback and foreign-key integrity

### Task 3: Implement Asterisk room provisioning

- [x] Add semantic provisioning operations backed by AMI/AstDB in `ami/`
- [x] Persist intent before side effects; serialize per-room changes and reject stale generation completions
- [x] Implement read-back verification, bounded retry and restart/reconnect reconciliation
- [x] Implement guarded deletion with busy rejection, admission closure and race-safe finalization
- [x] Test rejected operations, lost acknowledgements, partial success, stale completions and unrelated-registry isolation

### Task 4: Introduce a conference manager

- [x] Replace the singleton runtime with independent room runtimes and one event dispatcher
- [x] Route room events and attempt outcomes without competing AMI stream readers
- [x] Parameterize commands with a room identity and enforce participant/attempt ownership
- [x] Preserve pure call transitions, per-room sequence boundaries, mute and speaking behaviour
- [x] Bound resync work and clean up subscriptions/runtime resources when deletion completes
- [x] Test two rooms with the same dialled number, simultaneous operations, reconnect and late events

### Task 5: Add authorized room APIs and WebSocket subscriptions

- [x] Add admin-only catalogue and grant endpoints with transactional grant replacement
- [x] Add room-scoped user endpoints and snapshot contracts
- [x] Apply access checks to every command, list and socket; preserve origin/session protections
- [x] Stop snapshots and commands after access revocation, user disablement or admin demotion
- [x] Remove unscoped routes and prevent global participant/call lookup fallbacks
- [x] Test forged room IDs, cross-room resource IDs, direct admin API requests and open-socket revocation

### Task 6: Add administrator room management and user room selection

- [x] Add a Rooms section to the admin panel with name, number, synchronization status and errors
- [x] Add create/delete actions and a user-assignment checklist; explain busy deletion and pending operations
- [x] Add an accessible-room selector to the conference screen and a no-access empty state
- [x] Reconnect/fetch on selection change; cancel obsolete requests and ignore late snapshots for prior rooms
- [x] Keep the personal phonebook on the left and capture the target room when a batch invitation starts
- [x] Handle revoked access or deleted selection without displaying stale participant data
- [ ] Verify compact layout, loading/error states and two simultaneous browser sessions

### Task 7: Upgrade deployment, verify and document

- [x] Wire room stores, manager and provisioner at startup using the existing shared database and AMI client
- [x] Update the stand's dialplan, minimal AMI permissions and AstDB persistence
- [x] Remove `WB_ROOM` as a required runtime selector; document its one-time upgrade role
- [x] Update README, environment examples and deployment instructions for room range and managed admission
- [ ] On a disposable database/stand, create two rooms, assign two users, place calls and verify complete isolation
- [ ] Verify busy deletion, empty deletion, revoked access, direct dial-in rejection and admitted-before-delete races
- [ ] Restart backend and Asterisk during provisioning/deletion and verify eventual convergence without data loss
- [x] Run all validation commands and record actual results; do not mark unperformed manual checks complete


## Implementation and Verification — 2026-09-25

Implemented the room catalogue, administrator grants, room-scoped APIs and sockets,
independent conference runtimes, browser room selection, and AstDB reconciliation.
The stand uses Asterisk 20.6.0. Its persistent AstDB volume and managed admission
contexts are installed. Room numbers can be reused after deletion completes;
immutable bridge IDs and guarded tombstones protect the new room from old cleanup.

Passed `make lint`, `make test`, `go test -race ./...`, `make build`, and frontend
lint. Tests cover migration preservation/rollback, one-time legacy grants, number
reuse, uncertain provisioning outcomes, stale generations, pending deletion across
coordinator restart, admission races, cross-room attempts/events, forbidden APIs,
cross-room resource IDs, and WebSocket closure after access revocation.

Real Asterisk checks used a disposable database: two simultaneously occupied
bridges, independent participant lists and mute, busy deletion rejection, empty
creation/deletion, and rejected dial-in after deletion. Creating a room while
Asterisk was offline recovered after container recreation with an empty registry.
Restarting the backend restored all four remaining QA rooms. Browser checks covered
room selection, room creation, the grants editor, and compact layout. QA rooms and
calls were subsequently removed.

The working database was backed up and upgraded. Users, sessions and contacts were
compared with the backup and are unchanged. Legacy room 1000 is ready in SQLite
and registered in AstDB, with both existing users granted access. Westbridge runs
on 127.0.0.1:8080.

Unchecked combined validation tasks above retain their outstanding manual portions:
two independent browser login sessions, a timed real-SIP admission/deletion race,
and killing processes during the deletion window. Those race/restart policies are
covered by automated tests; no claim is made that these precise manual scenarios
were performed. Live checks used synthetic local channels, not human audio calls.
