package main

import (
	"encoding/base64"
	"testing"

	"github.com/linkasu/linka.plays-metric/internal/product"
)

func TestIdentityAudiencesLoadsBase64ProductMap(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(`{"linka-plays":"linka-plays-metric","linka-looks":"linka-looks-metric"}`))
	t.Setenv("IDENTITY_TELEMETRY_AUDIENCES_JSON", "")
	t.Setenv("IDENTITY_TELEMETRY_AUDIENCES_BASE64", encoded)
	audiences, err := identityAudiences()
	if err != nil {
		t.Fatal(err)
	}
	if audiences[product.LinkaPlays] != "linka-plays-metric" || audiences[product.LinkaLooks] != "linka-looks-metric" {
		t.Fatalf("audiences = %#v", audiences)
	}
}

func TestIdentityAudiencesRejectsUnknownProduct(t *testing.T) {
	t.Setenv("IDENTITY_TELEMETRY_AUDIENCES_JSON", `{"unknown":"unknown-metric"}`)
	t.Setenv("IDENTITY_TELEMETRY_AUDIENCES_BASE64", "")
	if _, err := identityAudiences(); err == nil {
		t.Fatal("unknown product audience was accepted")
	}
}

func TestCORSOriginsRequiresExactHTTPSOriginsInProduction(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://linka.su;https://linkatype.web.app")
	origins, err := corsOrigins("production")
	if err != nil || len(origins) != 2 {
		t.Fatalf("origins=%#v err=%v", origins, err)
	}
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://linka.su")
	if _, err := corsOrigins("production"); err == nil {
		t.Fatal("insecure production origin was accepted")
	}
}
