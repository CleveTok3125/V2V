package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/internal/tripcolor"
)

// signedVerifyURL builds a fully valid /api/trip/verify query against srv.
func signedVerifyURL(t *testing.T, srv *ChatServer, mutate func(q url.Values)) string {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 7)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	pubHex := hex.EncodeToString(pub)
	serverPub := strings.ToLower(srv.ServerID.PublicKey)
	var prev [32]byte
	text := "hello trip"
	msgHash := sha256.Sum256([]byte(text))
	payload := tripcolor.CanonicalPayload(serverPub, 1, prev[:], msgHash[:], pub, "Tester#eff8", 9, 0)
	sig := ed25519.Sign(priv, payload)
	_ = srv
	q := url.Values{
		"pub":          {pubHex},
		"seq":          {"1"},
		"prev":         {hex.EncodeToString(prev[:])},
		"sig":          {hex.EncodeToString(sig)},
		"msg_hash":     {hex.EncodeToString(msgHash[:])},
		"server_pub":   {serverPub},
		"display_name": {"Tester#eff8"},
		"text":         {text},
		"tmp_id":       {"9"},
		"reply_to":     {"0"},
	}
	if mutate != nil {
		mutate(q)
	}
	return "/api/trip/verify?" + q.Encode()
}

func testServerWithID(t *testing.T) *ChatServer {
	t.Helper()
	s := NewChatServer()
	s.ServerID = &ServerIdentity{PublicKey: strings.Repeat("ab", 32)}
	return s
}

// chdirRepoRoot runs the test with CWD at the module root so the
// relative webterm/verify.html path resolves exactly like production
// (Docker CWD=/app carries the webterm dir).
func chdirRepoRoot(t *testing.T) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(file))
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func doVerify(t *testing.T, srv *ChatServer, ip, accept, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = ip + ":1234"
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	srv.handleTripVerify(rec, req)
	return rec
}

func TestVerifyPageHTML(t *testing.T) {
	chdirRepoRoot(t)
	srv := testServerWithID(t)
	rec := doVerify(t, srv, "10.9.0.1", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		signedVerifyURL(t, srv, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"Kiểm tra Trip", "application/json", "Link API gốc", "JSON thô"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page must contain %q", want)
		}
	}
	if strings.Contains(body, "hello trip") {
		t.Fatal("page must not inline user text server-side; the browser fetches it")
	}
}

func TestVerifyJSONUnchanged(t *testing.T) {
	chdirRepoRoot(t)
	srv := testServerWithID(t)
	// Distinct client IPs: the handler rate-limits 200ms per IP.
	ips := map[string]string{"": "10.9.1.1", "*/*": "10.9.1.2", "application/json": "10.9.1.3"}
	for _, accept := range []string{"", "*/*", "application/json"} {
		rec := doVerify(t, srv, ips[accept], accept, signedVerifyURL(t, srv, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("accept %q: code = %d", accept, rec.Code)
		}
		var j map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil {
			t.Fatalf("accept %q: not JSON: %v", accept, err)
		}
		if j["valid"] != true || j["badge"] == "" {
			t.Fatalf("accept %q: unexpected body %v", accept, j)
		}
	}
}

func TestVerifyPageMissingFileFallback(t *testing.T) {
	dir := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	srv := testServerWithID(t)
	rec := doVerify(t, srv, "10.9.0.3", "text/html", signedVerifyURL(t, srv, nil))
	var j map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil {
		t.Fatalf("fallback must be JSON, got %q", rec.Body.String())
	}
	if j["valid"] != true {
		t.Fatalf("fallback body %v", j)
	}
}

func TestVerifyBadParamsShowPage(t *testing.T) {
	// Browsers always get the page (which renders the API error state
	// itself); raw JSON errors are only for API consumers.
	chdirRepoRoot(t)
	srv := testServerWithID(t)
	rec := doVerify(t, srv, "10.9.0.4", "text/html", "/api/trip/verify?pub=zz")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	rec2 := doVerify(t, srv, "10.9.0.6", "", "/api/trip/verify?pub=zz")
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("API code = %d", rec2.Code)
	}
	if ct := rec2.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("API errors stay JSON, content-type = %q", ct)
	}
}

func TestVerifyPageShowsSeq(t *testing.T) {
	// Regression pin: seq renders as a number for the page template.
	chdirRepoRoot(t)
	srv := testServerWithID(t)
	rec := doVerify(t, srv, "10.9.0.5", "", signedVerifyURL(t, srv, func(q url.Values) {
		q.Set("seq", strconv.Itoa(41))
	}))
	var j map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	// seq 41 with a sig made for seq 1 must fail honestly, not 500.
	if j["valid"] != false {
		t.Fatalf("tampered seq must be invalid: %v", j)
	}
}
