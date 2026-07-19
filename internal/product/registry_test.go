package product

import "testing"

func TestRegistryIsClosed(t *testing.T) {
	spec, ok := Lookup(LinkaPlays)
	if !ok || !spec.AllowsStream(StreamPlays) || !spec.AllowsGame("aquarium") {
		t.Fatal("LINKa Plays registry is incomplete")
	}
	if _, ok := Lookup("unknown"); ok || spec.AllowsStream("raw") || spec.AllowsGame("arbitrary-game") {
		t.Fatal("registry accepted an unknown compile-time value")
	}
}

func TestRegistryContainsEveryMetricsProduct(t *testing.T) {
	tests := map[ID]string{
		LinkaLooks: "start", LinkaPictures: "app_open", LinkaType: "say", LinkaPaperboard: "board_open",
		LinkaSite: "page_view", LinkaTTS: "tts_generated",
	}
	keys := make(map[string]ID, len(tests)+1)
	plays, _ := Lookup(LinkaPlays)
	keys[plays.OpaqueKey] = LinkaPlays
	for id, kind := range tests {
		spec, ok := Lookup(id)
		if !ok || !spec.AllowsStream(StreamProduct) || !spec.AllowsProductKind(kind) {
			t.Fatalf("product %q is incomplete", id)
		}
		if previous, duplicate := keys[spec.OpaqueKey]; duplicate {
			t.Fatalf("products %q and %q share an opaque key", previous, id)
		}
		keys[spec.OpaqueKey] = id
	}
}

func TestRegistryRejectsCrossProductKinds(t *testing.T) {
	looks, _ := Lookup(LinkaLooks)
	pictures, _ := Lookup(LinkaPictures)
	if looks.AllowsProductKind("set_import") || pictures.AllowsProductKind("openTobiiCalibration") {
		t.Fatal("registry accepted a kind from another product")
	}
}
