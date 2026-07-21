# Privacy Audit: Analytics Baseline

Status: remediation required before any legacy analytics is treated as a reporting source.

## Scope

The audit covers active application telemetry, server-side product metrics, the public donation site, and the target `linka.metrics` platform. The target platform accepts only typed closed contracts and does not accept text, account identifiers, payment identifiers, file paths, URLs, IP addresses, or arbitrary attributes.

## Findings

| System | Finding | Required remediation |
|---|---|---|
| LINKa Type PWA | Firebase analytics receives phrase text, predictor words, content-linked IDs, and Firebase UID. | Do not migrate historical records. Replace the client tracker with consent-first V2 outcome events before using Type data in reports. |
| LINKa Type Android/iOS | Consent defaults differ by platform. | Use one explicit opt-in state machine and disable all analytics SDK collection until consent is enabled. |
| LINKa Looks | The legacy metric activation flow sends an email address to the metric endpoint. | Move activation to a dedicated identity/activation flow. Telemetry must not receive email. |
| LINKa Pictures KMP | The visible metrics preference is not wired as the SDK collection gate. | Wire the preference before creating an analytics client and make the default `unknown` state non-collecting. |
| TTS | The service stores useful aggregate fields, but its raw metrics endpoint needs access review. | Restrict or remove raw record access. Export only server-side aggregate outcome events. |
| Paperboard | The release policy intentionally forbids usage analytics. | Keep that boundary. Add only minimal operational failures after a separate contract review. |
| nkolinka.ru | The site currently prohibits web analytics. | Do not add browser tracking until policy, consent UX, and source-policy tests are updated together. Server-side verified financial events remain a separate accounting flow. |

## Decisions Already Recorded

- Product telemetry is opt-in only.
- Pseudonymous event retention target is 36 months; deployment must set retention only after the legal owner approves this audit.
- Dashboards are internal only.
- Cross-product linkage is disabled unless a separate opt-in explicitly enables it.
- Role, age band, and organization are voluntary profile fields and cannot be inferred from behavior.
- The public product report uses the term "engagement and task-completion proxy", not communication or medical outcome.

## Release Gates

1. No legacy Firebase, SQLite, or Electron metric record may be copied into ClickHouse.
2. Every client starts with telemetry state `unknown`, sends no network telemetry, and exposes explicit enable/deny controls.
3. Contract tests reject text, email, account IDs, content IDs, paths, URLs, raw gaze data, and arbitrary JSON.
4. Privacy deletion covers every product telemetry table introduced by the release.
5. DataLens receives only `SQL SECURITY DEFINER` views without raw scope keys.
6. A release may enable a product only after an end-to-end test proves consent, ingestion, suppression, and DataLens filtering.

## Open Legal Approval

The data protection owner must approve the final consent copy, retention activation, staff access list, small-group suppression threshold, and the separation between statutory donation records and identity-free financial aggregates.
