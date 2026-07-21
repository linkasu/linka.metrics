# TTS Outcome Ingestion V2

`POST /internal/tts/outcomes` is an optional public-network service-to-service ingress for `tts-echo`. It is registered only when both `TTS_OUTCOME_HMAC_ACTIVE_SECRET` and `TTS_OUTCOME_SUBJECT_KEY` are configured. An absent active secret leaves the route unavailable and does not prevent a production collector from starting.

The route accepts `application/json` and `LINKA-HMAC-V2` headers signed with the independent `TTS_OUTCOME_HMAC_*` active or previous key. `X-Linka-Caller` must be exactly `tts-echo`; client `Authorization` and LINKa Identity are not used. `TTS_OUTCOME_SUBJECT_KEY` must be the configured lowercase 64-character opaque key. `TTS_OUTCOME_HMAC_ACTIVE_KEY_ID` defaults to `default` when omitted.

The body is a strict V2 batch. It must have `scope.product: "linka-tts"`, exactly the configured `scope.subject_key`, no `person_key` or `org_key`, and `stream: "outcome"`. `Idempotency-Key` and `X-Linka-Request-ID` must both exactly equal `batch_id`. After validation, collector forwards the exact accepted bytes to the existing writer V2 endpoint as caller `collector`.

Only the registered `linka-tts` outcome schema is accepted. It contains closed outcome enums and coarse buckets only. Raw synthesized text, request text, user identifiers, URLs, paths, provider request IDs, and arbitrary metadata are not accepted or logged.

## YC Deployment

The production workflow keeps this ingress disabled by default. To enable it, set GitHub variables `TTS_OUTCOME_SUBJECT_KEY` and, if not using `default`, `TTS_OUTCOME_HMAC_ACTIVE_KEY_ID`; add `tts_outcome_hmac_active_secret` to the configured Yandex Lockbox version. The workflow injects that Lockbox entry only when the subject variable is set, so deployments without this optional secret continue to start with the route absent.
