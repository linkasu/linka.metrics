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
)

type Spec struct {
	ID           ID
	OpaqueKey    string
	streams      map[Stream]struct{}
	gameIDs      map[string]struct{}
	productKinds map[string]struct{}
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
		streams: streamSet(StreamProduct),
		productKinds: stringSet(
			"start", "platformDetected", "openSettings", "openSet", "openFolder", "openEditor", "openTobiiCalibration",
			"cardClick", "toggleOutputLine", "toggleGazeLock", "share", "move", "trash", "editorAddImage", "editorAddAudio",
			"settingsToggleEyeExit", "settingsToggleEyeChoose", "settingsToggleEyeActivation", "settingsToggleEyePagination",
			"settingsToggleKeyboardActivation", "settingsToggleJoystickActivation", "settingsToggleTypeSound",
			"settingsToggleMouseActivation", "settingsTogglePageTurnMode", "settingsToggleEyeScale", "settingsSetTimeout",
			"tobiiCalibrationStart", "tobiiCalibrationPoint", "tobiiCalibrationFinish", "tobiiCalibrationCancel",
			"tobiiCalibrationError", "tobiiCalibrationApplySaved", "tobiiCalibrationApplySavedResult", "tobiiCalibrationUnavailable",
			"updateAvailable", "updateDownloaded", "updateError", "updateInstallConfirmed",
		),
	},
	LinkaPictures: {
		ID: LinkaPictures, OpaqueKey: "070210b29cd1c08ceb82a8c631463765db84aceed4a73181bf9d2e0e99968a58",
		streams: streamSet(StreamProduct),
		productKinds: stringSet(
			"app_open", "open_set", "edit_set", "create_set", "open_settings", "open_grid_settings", "resize_grid",
			"set_without_space", "add_card", "add_set", "card_select", "set_list_open", "set_open", "set_import",
			"set_export", "output_speak", "direct_play", "playback_failed", "quiz_answer", "match_pair", "editor_open",
			"set_save", "parent_code_check", "non_fatal_error",
		),
	},
	LinkaType: {
		ID: LinkaType, OpaqueKey: "074a5e8a5a5b103c9d7057f284eb3418d91870ced0941f9374edefdd78c6a6c8",
		streams: streamSet(StreamProduct),
		productKinds: stringSet(
			"app_open", "predicator_use", "spotlight", "say", "quickes_say", "bank_cselect", "bank_sselect", "login",
			"logout", "register", "update_prompt_shown", "update_accepted", "mobile_app_prompt_shown",
			"mobile_app_link_clicked", "bank_cache_started", "bank_cache_completed", "download_category_cache",
			"realtime_sync", "realtime_sync_error", "dialog_mode_opened", "dialog_mode_closed", "dialog_chat_create",
			"dialog_chat_select", "dialog_chat_delete", "dialog_message_send", "dialog_record_start", "dialog_record_stop",
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
		streams:      streamSet(StreamProduct),
		productKinds: stringSet("tts_generated"),
	},
}

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
