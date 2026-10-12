package clientcore

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeConn is a Conn that records the read limit the core sets.
type fakeConn struct{ limit int64 }

func (c *fakeConn) ReadJSON(any) error                { return nil }
func (c *fakeConn) WriteJSON(any) error               { return nil }
func (c *fakeConn) ReadMessage() (int, []byte, error) { return 0, nil, nil }
func (c *fakeConn) WriteMessage(int, []byte) error    { return nil }
func (c *fakeConn) SetReadLimit(n int64)              { c.limit = n }
func (c *fakeConn) Close() error                      { return nil }

// fakeTransport answers a scripted sequence of dials and records the URLs.
type fakeTransport struct {
	results []dialResult
	urls    []string
	conns   []*fakeConn
}

type dialResult struct {
	status int
	body   string
	err    error
}

func (t *fakeTransport) Dial(url string) (Conn, *http.Response, error) {
	t.urls = append(t.urls, url)
	r := t.results[0]
	t.results = t.results[1:]
	if r.err != nil {
		return nil, nil, r.err
	}
	if r.status != 0 && r.status != http.StatusSwitchingProtocols {
		resp := &http.Response{StatusCode: r.status, Body: io.NopCloser(strings.NewReader(r.body))}
		return nil, resp, errors.New("handshake failed")
	}
	c := &fakeConn{}
	t.conns = append(t.conns, c)
	return c, nil, nil
}

// fakeGate records what the core asked it to do.
type fakeGate struct {
	pass      string
	solveOK   bool
	solved    int
	solveURLs []string
}

func (g *fakeGate) Pass(string) string { return g.pass }
func (g *fakeGate) Solve(wsURL, pin string) (string, bool) {
	g.solved++
	g.solveURLs = append(g.solveURLs, wsURL)
	return "lease-pass", g.solveOK
}

// fakeOutput records the events.
type fakeOutput struct{ events []DialEvent }

func (o *fakeOutput) OnDial(e DialEvent) { o.events = append(o.events, e) }

func TestDialManagedSetsTheReadLimit(t *testing.T) {
	tr := &fakeTransport{results: []dialResult{{}}}
	d := Dialer{Transport: tr, Config: DialConfig{ReadLimit: 4096}}
	conn, url, err := d.DialManaged("ws://example.test/ws")
	if err != nil || conn == nil {
		t.Fatalf("dial = %v, %v", conn, err)
	}
	if url != "ws://example.test/ws" {
		t.Fatalf("url = %q", url)
	}
	if tr.conns[0].limit != 4096 {
		t.Fatalf("read limit = %d, want 4096", tr.conns[0].limit)
	}
}

func TestDialManagedUpgradesToTLSOnce(t *testing.T) {
	tr := &fakeTransport{results: []dialResult{{status: http.StatusUpgradeRequired}, {}}}
	out := &fakeOutput{}
	d := Dialer{Transport: tr, Config: DialConfig{ReadLimit: 1}, Output: out}
	if _, url, err := d.DialManaged("ws://example.test/ws"); err != nil {
		t.Fatal(err)
	} else if url != "wss://example.test/ws" {
		t.Fatalf("url = %q, want the upgraded one", url)
	}
	if len(tr.urls) != 2 || tr.urls[0] != "ws://example.test/ws" || tr.urls[1] != "wss://example.test/ws" {
		t.Fatalf("dials = %v", tr.urls)
	}
	if len(out.events) != 1 || out.events[0].Kind != DialUpgraded || out.events[0].URL != "wss://example.test/ws" {
		t.Fatalf("events = %+v", out.events)
	}
}

func TestDialManagedSolvesTheGateAndCarriesThePass(t *testing.T) {
	tr := &fakeTransport{results: []dialResult{{status: http.StatusTooManyRequests, body: `{"required":true}`}, {}}}
	g := &fakeGate{solveOK: true}
	out := &fakeOutput{}
	d := Dialer{Transport: tr, Gate: g, Config: DialConfig{ReadLimit: 1}, Output: out}
	if _, url, err := d.DialManaged("wss://example.test/ws"); err != nil {
		t.Fatal(err)
	} else if url != "wss://example.test/ws?gate_pass=lease-pass" {
		t.Fatalf("url = %q, want the lease pass appended", url)
	}
	if g.solved != 1 || g.solveURLs[0] != "wss://example.test/ws" {
		t.Fatalf("gate solved=%d urls=%v", g.solved, g.solveURLs)
	}
	if len(out.events) != 1 || out.events[0].Kind != DialGateRequired {
		t.Fatalf("events = %+v, want one gate-required before the successful retry", out.events)
	}
}

// A 429 that is not a gate challenge is a plain failure: the core must not
// burn attempts solving something that is not a puzzle.
func TestDialManagedNonGate429Fails(t *testing.T) {
	tr := &fakeTransport{results: []dialResult{{status: http.StatusTooManyRequests, body: `{"required":false}`}}}
	g := &fakeGate{solveOK: true}
	out := &fakeOutput{}
	d := Dialer{Transport: tr, Gate: g, Config: DialConfig{ReadLimit: 1}, Output: out}
	if _, _, err := d.DialManaged("wss://example.test/ws"); err == nil {
		t.Fatal("a non-gate 429 must fail the dial")
	}
	if g.solved != 0 {
		t.Fatalf("gate solved %d times, want none", g.solved)
	}
	last := out.events[len(out.events)-1]
	if last.Kind != DialFailed || last.Status != http.StatusTooManyRequests || !strings.Contains(last.Body, "required") {
		t.Fatalf("failure event = %+v", last)
	}
}

func TestDialManagedGivesUpWhenTheGateFails(t *testing.T) {
	tr := &fakeTransport{results: []dialResult{{status: http.StatusTooManyRequests, body: `{"required":true}`}}}
	g := &fakeGate{solveOK: false}
	d := Dialer{Transport: tr, Gate: g, Config: DialConfig{ReadLimit: 1, MaxAttempts: 3}}
	if _, _, err := d.DialManaged("wss://example.test/ws"); err == nil {
		t.Fatal("a refused gate must fail the dial")
	}
	if len(tr.urls) != 1 {
		t.Fatalf("dials = %v, want no retry after the gate gave up", tr.urls)
	}
}

func TestDialManagedExhaustsAttempts(t *testing.T) {
	tr := &fakeTransport{results: []dialResult{
		{status: http.StatusTooManyRequests, body: `{"required":true}`},
		{status: http.StatusTooManyRequests, body: `{"required":true}`},
	}}
	g := &fakeGate{solveOK: true}
	d := Dialer{Transport: tr, Gate: g, Config: DialConfig{ReadLimit: 1, MaxAttempts: 2}}
	if _, _, err := d.DialManaged("wss://example.test/ws"); err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("err = %v, want retries exhausted", err)
	}
}

// A negative limit is as unset as zero: SetReadLimit would read it as no
// limit too.
func TestDialManagedTreatsANegativeReadLimitAsUnset(t *testing.T) {
	tr := &fakeTransport{results: []dialResult{{}}}
	d := Dialer{Transport: tr, Config: DialConfig{ReadLimit: -1}}
	if _, _, err := d.DialManaged("ws://example.test/ws"); err != nil {
		t.Fatal(err)
	}
	if tr.conns[0].limit != defaultReadLimit {
		t.Fatalf("read limit = %d, want the default %d", tr.conns[0].limit, defaultReadLimit)
	}
}
