package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIVersionReturnsStamp(t *testing.T) {
	old := Version
	Version = "test-1.2.3"
	defer func() { Version = old }()

	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	rec := httptest.NewRecorder()
	handleAPIVersion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", cc)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "test-1.2.3" {
		t.Fatalf("version = %q, want test-1.2.3", body["version"])
	}
	if !strings.Contains(rec.Body.String(), "test-1.2.3") {
		t.Fatal("body must carry the stamp verbatim")
	}
}
