# Local Asterisk test stand

A single Asterisk 20 container with ConfBridge room **1000** and two softphone
endpoints, **1001** and **1002**. It exists so the full Westbridge flow — live
roster, kick, invite — can be exercised by hand against a real PBX.

## What is in here

| File | Purpose |
| --- | --- |
| `docker-compose.yml` | The Asterisk service, its published ports and config mounts |
| `asterisk/Dockerfile` | Asterisk 20 from the Ubuntu 24.04 archive (amd64 and arm64) |
| `asterisk/manager.conf` | AMI on 5038 with the `westbridge` user |
| `asterisk/http.conf` | Asterisk's HTTP server, deliberately disabled |
| `asterisk/pjsip.conf` | UDP transport plus endpoints 1001 and 1002 |
| `asterisk/confbridge.conf` | `default_bridge` / `default_user` profiles |
| `asterisk/extensions.conf` | `internal` (1000 → ConfBridge) and `conference-out` (invites) |
| `.env.example` | `WB_*` values matching this stand |

There is no official Asterisk image and the unofficial ones are mostly
amd64-only, so the container is built from the Ubuntu package. The first
`up` therefore compiles nothing but does download a base image and install
Asterisk — expect a minute or two.

## Bring it up

```sh
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml logs -f asterisk
```

If `docker` is not on your `PATH` on macOS, see the note in the top-level
[README](../README.md#docker-cli-missing-on-macos).

Check that AMI answers and that the config loaded:

```sh
nc 127.0.0.1 5038            # prints "Asterisk Call Manager/…", Ctrl-C to quit
docker compose -f deploy/docker-compose.yml exec asterisk asterisk -rx 'pjsip show endpoints'
docker compose -f deploy/docker-compose.yml exec asterisk asterisk -rx 'confbridge list'
docker compose -f deploy/docker-compose.yml exec asterisk asterisk -rx 'manager show users'
```

## Run the backend against it

```sh
cp deploy/.env.example deploy/.env
set -a; . ./deploy/.env; set +a
make build
.bin/westbridge bootstrap-admin admin  # first run only, prompts for password
.bin/westbridge
```

Then open <http://localhost:8080> and sign in as the administrator you created.
The example environment enables cookies over local HTTP; use HTTPS and
`WB_COOKIE_SECURE=true` outside local development. The backend starts even when the container
is down; the UI just reports Asterisk as disconnected until AMI comes back.

## Register a softphone

Any SIP client works — Linphone, Zoiper, Telephone.app, Blink.

| Setting | Value |
| --- | --- |
| Username / auth user | `1001` (or `1002`) |
| Password | `1001-secret` (or `1002-secret`) |
| Domain / SIP server | `127.0.0.1` port `5060` (use your LAN IP from another device) |
| Transport | UDP |

Then **dial `1000`**. The call lands in `ConfBridge(1000)` and the participant
must appear in the Westbridge roster within about a second. Verify from the
CLI too:

```sh
docker compose -f deploy/docker-compose.yml exec asterisk asterisk -rx 'confbridge list 1000'
```

To test the invite path, register the second softphone as `1002`, leave it
idle, and add `1002` from the Westbridge UI. Asterisk originates
`Local/1002@conference-out`, dials the endpoint, and drops the answered call
into the room.

## Known limitations

- **Addressing must match the clients.** The default external SIP and media
  address is `127.0.0.1` for softphones on this computer. For other devices,
  change both addresses in `asterisk/pjsip.conf` to the host's LAN IP and restart
  the container. Otherwise BYE and RTP can be sent to an unreachable address.
  The container subnet alone belongs in `local_net`.
- **RTP ports must match.** `asterisk/rtp.conf` uses 10000–10100, matching Docker.
  When changing the range, update both files. Lost calls without SIP BYE are
  terminated after 60 seconds without incoming RTP (300 seconds on hold).
  Clients using silence suppression must still send RTP/comfort noise within
  this interval, or the timeout must be adjusted.
- **The credentials here are throwaway.** `manager.conf` and `pjsip.conf` carry
  plaintext secrets on purpose; this stand is for a laptop, not a network
  anyone else can reach.
- **Publishing 101 UDP ports** for RTP makes `docker compose up` noticeably
  slow on Docker Desktop. If narrowing the range, update `asterisk/rtp.conf` as well.

## Tear it down

```sh
docker compose -f deploy/docker-compose.yml down
```
