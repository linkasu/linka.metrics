# Reversible telemetry consent

`POST /v2/consents` accepts only Identity-signed service JWTs: `subject_type: service`,
exact `scope: ["consent:write"]`, the configured product audience, `product` matching
the body and `sub` equal to the target `scope.subject_key`. This is the existing
Identity outbox convention, not a wildcard service principal. Client telemetry,
privacy and legacy tokens cannot write consent. Neither control scope nor its JWT
contains person/org keys or a consent epoch; the epoch is top-level in the DTO.

```json
{"schema_version":2,"request_id":"10000000-0000-4000-8000-000000000001","scope":{"product":"linka-pictures","subject_key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"consent_epoch":1,"version":1,"preference":"allowed","recorded_at":"2026-11-08T12:00:00.000Z"}
```

`Idempotency-Key` equals `request_id`. Unknown fields, duplicate JSON keys, invalid
UUIDs, zero/overflowing unsigned integers and timestamps without exactly three
fractional digits are rejected. Ordering uses the version, not the client clock;
old timestamps remain deliverable. Collector forwards the exact original bytes to
`POST /internal/v2/consents`, signed with the existing collector-to-writer HMAC.

Success is HTTP 200 and `{request_id,status:"completed",replayed:false}`. Exact-body
retries return `replayed:true` without reapplying history. Same request ID with
different bytes is 409, even for equivalent JSON. A lower version is saved as a
completed no-op receipt (`applied:false`), so an old revoke behind a newer grant
does not deadletter or regress state. Equal version/equal epoch/preference is also
a no-op; a conflicting equal version is 409. Non-stale epoch regressions and
same-epoch grants after denial are 409. A higher-version allowed refresh keeps
the same epoch while it remains allowed. Versions strictly increase for applied
transitions within each `(product,subject_key)`; re-enabling after denial requires
a higher epoch.

| Current state | Control | Result |
| --- | --- | --- |
| unknown | allowed epoch 1, version 1 | active epoch 1 |
| unknown | denied epoch 1, version 1 | closed epoch 1 |
| allowed epoch 1 | allowed epoch 1, higher version | active epoch 1, refreshed revision |
| allowed epoch 1 | denied epoch 1, higher version | closed epoch 1 |
| denied epoch 1 | allowed epoch 1, higher version | 409, never reopened |
| denied epoch 1 | allowed epoch 2, higher version | active epoch 2 |
| active epoch 2 | older-version control | completed no-op |
| subject-wide global opt-out/delete | new grant | 403; suppression is never cleared |

Batch scope and Identity telemetry JWT gain optional positive uint64
`consent_epoch`, with exact presence/value matching. Missing epoch works only for
subjects with no applied consent ledger. Managed subjects require the latest
allowed epoch, including retries of completed or reserved batches. Revoked/old,
unknown and future epochs fail closed. Epoch zero is storage-only legacy data,
not a valid explicit protocol epoch. Subject/installation identity never rotates.

Safe DataLens v2/v3/product/outcome/quality projections filter epochs at read time;
derived session/daily views and analytical computation inherit those filters.
Legacy epoch-zero history becomes permanently hidden on the first managed
control. A new allowed epoch exposes only that epoch, never older history.
Subject hashing is unchanged, so consent cycles cannot inflate unique installs.
Analytical snapshots include applied consent in the privacy revision and are
invalidated on consent transitions. Existing expiry and subject/person/org
suppression predicates remain authoritative across every epoch.

Controls deliberately cannot discover or change person/org membership. Those
suppressions continue to reject matching authenticated batch scopes and hide
matching historical rows; an installation grant never deactivates them.

Privacy deletion continues deleting events and batch/record registries across
all epochs, without an epoch predicate. Consent ledger rows are retained as
minimal safety tombstones (opaque scope, version/epoch/preference, request ID,
digest and timestamps; no request payload). Removing these would permit delayed
controls and exact-body replays to resurrect state. Ledger rows have no TTL.
All active privacy suppressions, including completed deletes with an old worker's
expiry, are also permanent; no grant can deactivate them.

## Deployment and recovery constraints

1. Take/verify a native backup with ingestion/control writers paused. Preserve
   consent receipts and active suppressions together; never restore a pre-consent
   archive over a managed database or truncate the ledger to recover service.
2. Apply additive migration **019** before the new writer. Historical migrations
   are unchanged. Existing rows get default epoch zero, with no expiry backfill.
   Active suppression TTL is tightened; ordinary telemetry retention is unchanged.
   Reapply `deploy/clickhouse/init/010-users.sh` after migration to grant the writer
   SELECT/INSERT on `consent_ledger_v2`. Do not grant DataLens the ledger/state view.
3. Run the schema preflight, deploy exactly one consent-aware writer, then the
   collector. `v2Mu` serializes control/ingest/privacy creation inside that writer,
   not across multiple writer replicas. The new binary on schema 018 fails closed
   (missing ledger/columns); schema-first is mandatory.
4. Gateway `infra/terraform/main.tf` already forwards `/{proxy+}` to collector,
   including `/v2/consents`; writer ingress `Caddyfile` forwards the internal path
   unchanged. No extra wildcard, route rewrite or public internal endpoint is
   introduced. Verify the deployed gateway still matches this configuration.
5. Enable Identity delivery only after collector/writer/schema/privileges are
   ready. Identity must serialize versions and epochs per stable installation
   key, emit scoped denied (not legacy global opt-out) for managed off, bootstrap
   unknown off as denied epoch 1, and wait for its latest completed outbox receipt
   before issuing telemetry tokens. Legacy globally denied subjects cannot be
   relabelled as reversibly enabled. Those Identity changes are external to this repo.
6. Old binaries remain insert-compatible with schema 019 by omitting defaulted
   epoch columns; legacy unmanaged data keeps its old behavior. However old
   writers do not enforce consent at ingestion, and an old migration runner
   rejects unknown migration 019. After management starts, do not roll the writer
   or analytical snapshot service back to a consent-unaware binary. Pause ingress
   and roll forward instead. Never undo the safe views or delete tombstones.

Focused integration tests use an isolated temporary database and the existing
`CLICKHOUSE_INTEGRATION_ADDR/USER/PASSWORD` convention. CI runs them on pinned
ClickHouse 25.3; no production operation is needed for verification.
