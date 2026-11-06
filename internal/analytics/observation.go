package analytics

import "time"

// Observation is an already eligible fact. Subject is a product-local account
// alias, or a permitted source subject for the separate installation methodology.
// Useful denotes a successful primary outcome and also implies activity.
type Observation struct {
	Product      string
	Subject      string
	Installation string
	At           time.Time
	Activity     bool
	Useful       bool
}
