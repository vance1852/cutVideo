# cutVideo

cutVideo is a backend service for cloud based video post production. It carries a
cut from ingested camera footage to a distributed master: footage is registered
and checksum verified, editors assemble ordered timeline versions, a sealed
version is queued onto a shared render farm, background workers hold exclusive
encoder seats while they render, and finished masters are pushed to downstream
destinations with per destination bookkeeping.

## Business paths

Two public paths depend on each other:

1. **Ingest and assembly** — register footage, verify it against the transfer
   manifest, open a draft timeline version, add ordered clips per track, seal the
   version. Sealing validates every referenced asset, computes the program
   duration and advances the project pointer inside one transaction.
2. **Render and distribution** — submit a sealed version to the farm, reserve an
   exclusive seat, render with lease renewal and bounded retries, record the
   artefact, then dispatch it to every enabled destination and confirm each one
   independently.

## Domain rules worth knowing

- Only `verified` footage inside its retention window may back a clip.
- A project has at most one open draft; sealing supersedes the previous sealed
  version and moves the project pointer in the same transaction.
- A timeline version accepts clip changes only while it is a draft; concurrent
  writers are rejected with an optimistic `row_version` guard.
- A render seat is exclusive. Reservation is a single conditional update, so two
  workers can never share one seat.
- Render jobs consume one attempt per assignment, back off between attempts and
  become permanently failed once the attempt budget is spent.
- A worker refreshes the lease of the job it is encoding on a fixed interval, so
  a long render keeps the seat it already owns.
- An abandoned lease is recovered by the reaper: the job is requeued without
  burning an extra attempt and the seat is released.
- Cancellation is allowed for the requester and for supervisors, never after the
  job finished.
- Delivery is per destination: one refused destination does not discard the
  confirmations already collected, and disabled destinations are skipped.
- Every mutating action writes an audit event bound to actor, object, result and
  request id.

## Roles

| Role | Authority |
| --- | --- |
| `editor` | Owns their own projects, footage, timelines and render submissions. |
| `supervisor` | Everything an editor can do, plus farm arbitration, seat provisioning, delivery destinations and account management. |
| `auditor` | Read only access to projects, renders and the audit trail. |

Sessions are server side and revocable. Only the token hash is persisted; sign
out revokes the session immediately, and a supervisor can revoke every session of
one operator.

## Layout

```text
cmd/server              process entry point, signal handling, graceful shutdown
internal/app            dependency wiring, bootstrap, run loop
internal/config         environment configuration and validation
internal/domain         entities, state machines, invariants
internal/repository     persistence contracts
internal/storage/sqlite SQLite implementation, transactions, migrations runner
internal/service/auth   identity, sessions, roles
internal/service/media  footage ingest and verification
internal/service/editing projects and timeline versions
internal/service/render  queue, seats, leases, retries
internal/service/delivery destinations and dispatch, outbound HTTP
internal/worker         render worker pool and housekeeping reaper
internal/httpapi        routes, request decoding, response and error envelopes
internal/middleware     request id, access log, recovery, timeout, auth
internal/audit          audit event recorder
internal/idempotency    replay guard for mutating requests
internal/clock          controllable clock and business timezone
internal/ids            identifier and token generation
internal/logging        structured logging with redaction
migrations              embedded, versioned SQL schema
```

## Data model

Eight related tables plus schema bookkeeping: `users`, `sessions`, `projects`,
`media_assets`, `timeline_versions`, `timeline_clips`, `render_slots`,
`render_jobs`, `delivery_targets`, `delivery_records`, `audit_events` and
`idempotency_keys`. Foreign keys are enforced, business uniqueness is expressed
as unique indexes (project code, asset manifest digest per project, timeline
version per project, clip order per track, render idempotency key per project,
delivery record per job and destination), and every timestamp is stored as
milliseconds since the epoch.

Migrations are embedded in the binary and applied in version order. Re-running
them is a no-op, a mismatch between a recorded name and the embedded name is
reported instead of being patched, and a database created by a newer binary is
refused rather than downgraded.

## Running it

```bash
cp .env.example .env
export $(grep -v '^#' .env | xargs)          # or use your process manager
go run ./cmd/server
```

With `CUTVIDEO_BOOTSTRAP_EMAIL` and `CUTVIDEO_BOOTSTRAP_PASSWORD` set, the first
start creates one supervisor account and two render seats. Both steps are
skipped on an installed system.

```bash
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
curl -s -X POST localhost:8080/api/v1/sessions \
  -H 'Content-Type: application/json' \
  -d '{"email":"supervisor@example.com","password":"..."}'
```

Container:

```bash
docker build -t cutvideo:local .
docker run --rm -p 8080:8080 cutvideo:local
```

## HTTP surface

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/v1/sessions` | Sign in and receive a bearer token |
| DELETE | `/api/v1/sessions/current` | Revoke the current session |
| GET | `/api/v1/sessions/current` | Describe the authenticated principal |
| POST | `/api/v1/users` | Provision an operator (supervisor) |
| DELETE | `/api/v1/users/{userID}/sessions` | Revoke every session of an operator |
| POST/GET | `/api/v1/projects` | Create and list cut projects |
| GET | `/api/v1/projects/{projectID}` | Read one project |
| POST | `/api/v1/projects/{projectID}/lock` | Freeze editorial changes |
| POST | `/api/v1/projects/{projectID}/assets` | Register footage |
| GET | `/api/v1/projects/{projectID}/assets` | List footage with filters |
| POST | `/api/v1/assets/{assetID}/verify` | Verify against the manifest digest |
| POST | `/api/v1/assets/verify-batch` | Verify several assets, partial failures reported |
| POST | `/api/v1/assets/{assetID}/quarantine` | Remove footage from editorial use |
| POST | `/api/v1/projects/{projectID}/timelines` | Open the next draft version |
| POST | `/api/v1/timelines/{timelineID}/clips` | Add an ordered clip |
| DELETE | `/api/v1/timelines/{timelineID}/clips/{clipID}` | Remove a clip |
| POST | `/api/v1/timelines/{timelineID}/seal` | Seal a draft version |
| POST/GET | `/api/v1/renders` | Submit and list render jobs |
| POST | `/api/v1/renders/{jobID}/cancel` | Cancel unfinished work |
| GET | `/api/v1/render-farm/capacity` | Farm utilisation |
| POST | `/api/v1/render-farm/slots` | Provision a seat (supervisor) |
| POST | `/api/v1/projects/{projectID}/delivery-targets` | Create a destination (supervisor) |
| PATCH | `/api/v1/delivery-targets/{targetID}` | Enable or disable a destination |
| POST | `/api/v1/renders/{jobID}/deliveries/dispatch` | Push a master downstream |
| GET | `/api/v1/renders/{jobID}/deliveries` | Delivery records for a render |
| GET | `/api/v1/audit-events` | Query the audit trail |

Errors always use the same envelope:

```json
{"error":{"code":"precondition_failed","message":"render submission requires a sealed version","request_id":"req_..."}}
```

`POST /api/v1/renders` honours an `Idempotency-Key` header: replaying the same
key returns the original job instead of consuming farm capacity twice.

## Verification

```bash
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
```

Integration tests run against real SQLite files in temporary directories, drive
the HTTP surface end to end, exercise worker retries, cancellation, lease
recovery, restart recovery and concurrent seat reservation.
