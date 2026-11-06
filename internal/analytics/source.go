package analytics

// Source identifies a methodology; sources must not be silently combined.
type Source string

const (
	SourceAccountV3      Source = "account-v3"
	SourceInstallationV2 Source = "installation-v2"
	SourceGA4            Source = "ga4"
	SourceTTS            Source = "tts"
)

// Unit describes counted identifiers, not people. GA4 units are property-local;
// TTS units require stable unique operation IDs and are not reach units.
type Unit string

const (
	UnitProductAccount     Unit = "product_account"
	UnitInstallation       Unit = "installation"
	UnitPropertyIdentifier Unit = "property_identifier"
	UnitServiceOperation   Unit = "service_operation"
)
