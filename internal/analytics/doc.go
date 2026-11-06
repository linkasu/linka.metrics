// Package analytics calculates exact activity and mature funnels from observations.
//
// Callers must supply only committed, deduplicated, privacy-filtered observations:
// expired, suppressed, opted-out and otherwise ineligible facts must be removed
// before calling these helpers. This package is not a privacy enforcement layer.
// Callers classify activity and successful primary useful outcomes according to
// the product/source contract; technical events have neither flag set.
//
// A calculation must use one source, methodology and coverage interval. Callers
// retain source status and left-censoring metadata: first observed is not signup,
// installation or first-ever use. Source gaps or opt-out within a funnel window
// require an unknown/excluded classification outside these numeric helpers, not
// a failure inferred from missing events. A zero result is meaningful only for
// known, eligible coverage; it does not imply that unknown data is zero.
//
// Units are accounts, installations, property identifiers or service operations,
// never exact people. Distinct totals require the union of observations, not sums
// of daily distincts or overlapping version/platform breakdowns. GA4 retains its
// source timezone and property boundaries; these UTC helpers do not reconstruct
// GA4 funnels from aggregate cells or treat TTS operations as human reach.
package analytics
