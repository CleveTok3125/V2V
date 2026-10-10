package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAPIVersionCarriesOnlyTheStamp keeps /api/version to its documented
// contract. It is the release stamp endpoint, and a client reads it to
// decide whether it may talk to this server at all, so anything riding
// along here is a change to that contract.
func TestAPIVersionCarriesOnlyTheStamp(t *testing.T) {
	pairServer(t)
	rec := httptest.NewRecorder()
	handleAPIVersion(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("version payload is not JSON: %v (%s)", err, rec.Body)
	}
	if len(body) != 1 {
		t.Fatalf("version payload has %v, want only the stamp", body)
	}
	if _, ok := body["version"]; !ok {
		t.Fatalf("version payload = %v, want a version key", body)
	}
}

// TestAPICapabilitiesReportsPasskey: a client asks before it opens a
// browser, because a link that cannot work wastes the person's time.
func TestAPICapabilitiesReportsPasskey(t *testing.T) {
	pairServer(t) // enables WAConfig
	rec := httptest.NewRecorder()
	handleAPICapabilities(rec, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	var body struct {
		Passkey bool `json:"passkey"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("payload is not JSON: %v (%s)", err, rec.Body)
	}
	if !body.Passkey {
		t.Error("passkey reported off while WAConfig.Enabled is true")
	}
}

// TestAPICapabilitiesPasskeyOff: the answer has to change with the setting,
// or a client would be told the link will work and then watch it fail.
func TestAPICapabilitiesPasskeyOff(t *testing.T) {
	pairServer(t)
	WAConfig.Enabled = false

	rec := httptest.NewRecorder()
	handleAPICapabilities(rec, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["passkey"] != false {
		t.Fatalf("passkey = %v, want false", body["passkey"])
	}
}

// TestAPICapabilitiesIsABool: a client that read it as text would treat
// "false" as enabled, which is the wrong way to be wrong.
func TestAPICapabilitiesIsABool(t *testing.T) {
	pairServer(t)
	rec := httptest.NewRecorder()
	handleAPICapabilities(rec, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))

	raw := rec.Body.String()
	if raw == "" {
		t.Fatal("empty payload")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["passkey"].(bool); !ok {
		t.Fatalf("passkey is not a JSON bool: %s", raw)
	}
}
