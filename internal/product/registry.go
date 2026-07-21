package product

type ID string

const (
	LinkaPlays      ID = "linka-plays"
	LinkaLooks      ID = "linka-looks"
	LinkaPictures   ID = "linka-pictures"
	LinkaType       ID = "linka-type"
	LinkaPaperboard ID = "linka-paperboard"
	LinkaSite       ID = "linka-site"
	LinkaTTS        ID = "linka-tts"
)

type Stream string

const (
	StreamCommon    Stream = "common"
	StreamTechnical Stream = "technical"
	StreamPlays     Stream = "plays"
	StreamProduct   Stream = "product"
	StreamOutcome   Stream = "outcome"
)

type OutcomeRule struct {
	Results         []string
	Sources         []string
	Modes           []string
	CountBuckets    []string
	DurationBuckets []string
	FailureCodes    []string
}

type Spec struct {
	ID           ID
	OpaqueKey    string
	streams      map[Stream]struct{}
	gameIDs      map[string]struct{}
	productKinds map[string]struct{}
	outcomeRules map[string]OutcomeRule
}

var registry = map[ID]Spec{
	LinkaPlays: {
		ID:        LinkaPlays,
		OpaqueKey: "a0f1ccdc0e30ce4f267cb358ab74be5ce227f04c744d81fbaf2e2ac59893e37c",
		streams: map[Stream]struct{}{
			StreamCommon: {}, StreamTechnical: {}, StreamPlays: {},
		},
		gameIDs: stringSet(
			"aquarium", "balloons", "bells", "breathing-flower", "wake-owl", "clouds", "leaves-wind", "kite",
			"firefly-meadow", "catch-light", "starry-sky", "magic-dust", "light-gallery", "soap-circles",
			"northern-lights", "sun-rays", "snowflakes", "moon-path", "lighthouse", "sand-garden", "sea-shells",
			"paper-lanterns", "open-door", "warm-window", "warm-fire", "big-cards", "color-circle", "feed-animal",
			"butterfly", "flowers", "bubble-pop", "ducks", "fishes", "jellyfish", "frog", "hide-and-seek",
			"who-hiding", "find-color", "find-shape", "match-same", "what-missing", "follow-cue", "find-letter",
			"find-digit", "logic-pairs", "shadow-match", "sound-source", "odd-one-out", "find-emotion", "letter-hunt",
			"find-animal", "memory-cards", "gaze-maze", "build-robot", "pyramid", "dress-character", "train-sequence",
			"sandwich", "patterns", "color-pattern", "day-routine", "three-frame-story", "first-then", "musical-path",
			"mosaic", "shape-dance", "soup-recipe", "comic-strip", "schedule", "build-bridge", "shelf-sorting",
			"solfege", "choose-emotion", "choose-picture", "eat-or-not-eat", "word-categories", "yes-no", "i-want",
			"want-dont-want", "object-action", "where-object", "big-small", "one-many", "who-is-this", "opposites",
			"what-first", "mini-dialog", "social-phrases", "type-word", "clock", "calendar", "count-items",
			"coin-counting", "pizza-fractions", "greater-less", "scales", "number-line", "number-sorting", "sudoku-2x2",
			"lines-angles", "simple-graphs", "number-bonds", "shop", "coordinates", "shapes", "color-shape",
			"math-actions", "minesweeper-safe", "domino-matching", "number-2048", "sliding-puzzle", "uno-like",
			"step-tetris", "sokoban-large", "tic-tac-toe", "connect-four", "reversi-light", "lines-five",
			"checkers-light", "chess-mini", "battleship-light", "step-pong", "route-snake", "cursor-magnet", "boat",
			"gaze-follow-snake", "table-tennis", "road-car", "glider", "line-drawing", "rails", "balancer",
			"snow-trail", "robot-vacuum", "garden-watering", "space-orbit",
		),
	},
	LinkaLooks: {
		ID: LinkaLooks, OpaqueKey: "a2aea6a7de105d4001e90f53cb24388163609c7721eb947d24327432c21901df",
		streams: streamSet(StreamProduct, StreamOutcome),
		productKinds: stringSet(
			"start", "platformDetected", "openSettings", "openSet", "openFolder", "openEditor", "openTobiiCalibration",
			"cardClick", "toggleOutputLine", "toggleGazeLock", "share", "move", "trash", "editorAddImage", "editorAddAudio",
			"settingsToggleEyeExit", "settingsToggleEyeChoose", "settingsToggleEyeActivation", "settingsToggleEyePagination",
			"settingsToggleKeyboardActivation", "settingsToggleJoystickActivation", "settingsToggleTypeSound",
			"settingsToggleMouseActivation", "settingsTogglePageTurnMode", "settingsToggleEyeScale", "settingsSetTimeout",
			"settingsToggleAnimation",
			"tobiiCalibrationStart", "tobiiCalibrationPoint", "tobiiCalibrationFinish", "tobiiCalibrationCancel",
			"tobiiCalibrationError", "tobiiCalibrationApplySaved", "tobiiCalibrationApplySavedResult", "tobiiCalibrationUnavailable",
			"updateAvailable", "updateDownloaded", "updateError", "updateInstallConfirmed", "deploySmoke",
		),
		outcomeRules: outcomeRules(
			"utterance_completed", OutcomeRule{Results: []string{"completed", "failed", "cancelled"}, Modes: []string{"standard", "direct", "without-space"}, CountBuckets: countBuckets, DurationBuckets: durationBuckets, FailureCodes: playbackFailureCodes},
			"exercise_completed", OutcomeRule{Results: []string{"completed", "incomplete", "failed"}, Sources: []string{"quiz", "match"}, CountBuckets: countBuckets, DurationBuckets: durationBuckets, FailureCodes: exerciseFailureCodes},
			"set_saved", OutcomeRule{Results: []string{"completed", "failed"}, Sources: []string{"created", "edited"}, CountBuckets: countBuckets, FailureCodes: setFailureCodes},
			"transfer_completed", OutcomeRule{Results: []string{"completed", "failed"}, Sources: []string{"import", "export"}, FailureCodes: transferFailureCodes},
			"gaze_calibration_completed", OutcomeRule{Results: []string{"completed", "failed", "cancelled"}, FailureCodes: gazeFailureCodes},
		),
	},
	LinkaPictures: {
		ID: LinkaPictures, OpaqueKey: "070210b29cd1c08ceb82a8c631463765db84aceed4a73181bf9d2e0e99968a58",
		streams: streamSet(StreamProduct, StreamOutcome),
		productKinds: stringSet(
			"app_open", "open_set", "edit_set", "create_set", "open_settings", "open_grid_settings", "resize_grid",
			"set_without_space", "add_card", "add_set", "card_select", "set_list_open", "set_open", "set_import",
			"set_export", "output_speak", "direct_play", "playback_failed", "quiz_answer", "match_pair", "editor_open",
			"set_save", "parent_code_check", "non_fatal_error",
		),
		outcomeRules: outcomeRules(
			"utterance_completed", OutcomeRule{Results: []string{"completed", "failed", "cancelled"}, Modes: []string{"standard", "direct", "without-space"}, CountBuckets: countBuckets, DurationBuckets: durationBuckets, FailureCodes: playbackFailureCodes},
			"exercise_completed", OutcomeRule{Results: []string{"completed", "incomplete", "failed"}, Sources: []string{"quiz", "match"}, CountBuckets: countBuckets, DurationBuckets: durationBuckets, FailureCodes: exerciseFailureCodes},
			"set_saved", OutcomeRule{Results: []string{"completed", "failed"}, Sources: []string{"created", "edited"}, CountBuckets: countBuckets, FailureCodes: setFailureCodes},
			"transfer_completed", OutcomeRule{Results: []string{"completed", "failed"}, Sources: []string{"import", "export"}, FailureCodes: transferFailureCodes},
		),
	},
	LinkaType: {
		ID: LinkaType, OpaqueKey: "074a5e8a5a5b103c9d7057f284eb3418d91870ced0941f9374edefdd78c6a6c8",
		streams: streamSet(StreamProduct, StreamOutcome),
		productKinds: stringSet(
			"app_open", "predicator_use", "spotlight", "say", "quickes_say", "bank_cselect", "bank_sselect", "login",
			"logout", "register", "update_prompt_shown", "update_accepted", "mobile_app_prompt_shown",
			"mobile_app_link_clicked", "bank_cache_started", "bank_cache_completed", "download_category_cache",
			"realtime_sync", "realtime_sync_error", "dialog_mode_opened", "dialog_mode_closed", "dialog_chat_create",
			"dialog_chat_select", "dialog_chat_delete", "dialog_message_send", "dialog_record_start", "dialog_record_stop",
		),
		outcomeRules: outcomeRules(
			"phrase_composed", OutcomeRule{Sources: []string{"input", "quick", "bank", "dialog"}, CountBuckets: countBuckets},
			"speech_completed", OutcomeRule{Results: []string{"completed", "failed", "cancelled"}, Sources: []string{"input", "quick", "bank", "dialog"}, Modes: []string{"local", "cloud"}, CountBuckets: countBuckets, DurationBuckets: durationBuckets, FailureCodes: playbackFailureCodes},
			"bank_action_completed", OutcomeRule{Results: []string{"completed", "failed"}, Sources: []string{"phrase_inserted", "phrase_spoken", "reader_opened"}, FailureCodes: setFailureCodes},
			"dialog_action_completed", OutcomeRule{Results: []string{"completed", "failed"}, Sources: []string{"message_sent", "suggestion_accepted", "suggestion_dismissed"}, FailureCodes: setFailureCodes},
			"sync_completed", OutcomeRule{Results: []string{"completed", "failed"}, CountBuckets: countBuckets, FailureCodes: syncFailureCodes},
		),
	},
	LinkaPaperboard: {
		ID: LinkaPaperboard, OpaqueKey: "faaa4e383af4af3c458ce9178d4a87c2e2f314407e70807555507b71467031e1",
		streams:      streamSet(StreamProduct),
		productKinds: stringSet("app_open", "board_open", "settings_open", "symbol_selected", "phrase_spoken"),
	},
	LinkaSite: {
		ID: LinkaSite, OpaqueKey: "a1fd852a6ad52468c6ad95b47df33966052a4fc3ff44334e5bcfc4f03e24c372",
		streams:      streamSet(StreamProduct),
		productKinds: stringSet("page_view"),
	},
	LinkaTTS: {
		ID: LinkaTTS, OpaqueKey: "821617fc2acf9c0e033286139584ae3ae920a85c18e04927df929784883f9b8e",
		streams:      streamSet(StreamProduct, StreamOutcome),
		productKinds: stringSet("tts_generated"),
		outcomeRules: outcomeRules(
			"request_completed", OutcomeRule{Results: []string{"completed", "failed", "cancelled"}, Sources: []string{"yandex", "sber", "local"}, CountBuckets: countBuckets, DurationBuckets: durationBuckets, FailureCodes: ttsFailureCodes},
			"cache_operation", OutcomeRule{Results: []string{"hit", "miss", "evicted"}},
		),
	},
}

var (
	countBuckets         = []string{"one", "two_to_five", "six_to_twenty", "more_than_twenty"}
	durationBuckets      = []string{"under_5s", "5s_to_30s", "31s_to_2m", "over_2m"}
	playbackFailureCodes = []string{"engine_unavailable", "request_failed", "timeout", "cancelled"}
	exerciseFailureCodes = []string{"state_invalid", "media_unavailable", "interrupted"}
	setFailureCodes      = []string{"validation_failed", "storage_failed", "permission_denied"}
	transferFailureCodes = []string{"format_invalid", "media_missing", "storage_failed", "permission_denied"}
	gazeFailureCodes     = []string{"device_unavailable", "calibration_failed", "permission_denied"}
	syncFailureCodes     = []string{"network_unavailable", "conflict", "server_error"}
	ttsFailureCodes      = []string{"provider_unavailable", "quota_exceeded", "request_failed", "timeout", "cancelled"}
)

func Lookup(id ID) (Spec, bool) {
	spec, ok := registry[id]
	return spec, ok
}

func (s Spec) AllowsStream(stream Stream) bool {
	_, ok := s.streams[stream]
	return ok
}

func (s Spec) AllowsGame(gameID string) bool {
	_, ok := s.gameIDs[gameID]
	return ok
}

func (s Spec) AllowsProductKind(kind string) bool {
	_, ok := s.productKinds[kind]
	return ok
}

func (s Spec) OutcomeRule(kind string) (OutcomeRule, bool) {
	rule, ok := s.outcomeRules[kind]
	return rule, ok
}

func streamSet(values ...Stream) map[Stream]struct{} {
	result := make(map[Stream]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func outcomeRules(values ...any) map[string]OutcomeRule {
	result := make(map[string]OutcomeRule, len(values)/2)
	for index := 0; index < len(values); index += 2 {
		result[values[index].(string)] = values[index+1].(OutcomeRule)
	}
	return result
}
