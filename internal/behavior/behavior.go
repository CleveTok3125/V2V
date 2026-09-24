// Package behavior scores client behavior from timing/count metadata
// only: connection/message/history/HTTP events, auth and protocol
// anomalies. Message content is never read, stored or needed. Profiles
// are keyed by client IP (guests mint cheap identities, so anything
// weaker would be trivially rotated); an authenticated identity only
// lowers scrutiny, never creates a separate profile.
//
// All tuning lives in caller-supplied params: feature weights, sample
// floors, ramp bounds, windows and tier thresholds come from config,
// never from literals here. The fold shape itself (ramp directions,
// the weighted average, the convex curve, the group mix) is fixed in
// code; only its parameters are configurable.
package behavior

import "time"

// maxRing caps every per-profile event ring. Counters stay exact; only
// the distributional tail is truncated.
const maxRing = 512

// IdentityKind ranks how costly an identity is to mint. Stronger
// identities only lower suspicion, per the identity feature.
type IdentityKind int

const (
	IdGuest IdentityKind = iota
	IdTrip
	IdKey
)

// HTTPEvent records one non-chat request: class, status and time only.
// Query strings and bodies are never captured.
type HTTPEvent struct {
	Time   time.Time
	Class  string
	Status int
}

// ErrEvent records one auth or protocol anomaly by code and time.
type ErrEvent struct {
	Time time.Time
	Code string
}

// Profile is the ephemeral per-IP behavior record.
type Profile struct {
	FirstSeen time.Time
	LastSeen  time.Time
	Conns     []time.Time
	Discs     []time.Time
	Msgs      []time.Time
	HistReq   []time.Time
	HTTP      []HTTPEvent
	Errs      []ErrEvent
	Identity  IdentityKind
	// IdentityCount tracks distinct display/identity keys seen from
	// this IP: many cheap identities on one IP is itself a signal.
	IdentityCount int
	Blocklisted   bool
	TotalConns    int64
	TotalMsgs     int64
	// Tier is the hysteresis state from the last scoring.
	Tier int
}

func appendCappedTime(dst []time.Time, t time.Time) []time.Time {
	dst = append(dst, t)
	if len(dst) > maxRing {
		dst = dst[len(dst)-maxRing:]
	}
	return dst
}

func (p *Profile) touch(t time.Time) {
	if p.FirstSeen.IsZero() {
		p.FirstSeen = t
	}
	p.LastSeen = t
}

// AddConnect records a new connection.
func (p *Profile) AddConnect(t time.Time) {
	p.touch(t)
	p.Conns = appendCappedTime(p.Conns, t)
	p.TotalConns++
}

// AddDisconnect records a disconnect.
func (p *Profile) AddDisconnect(t time.Time) {
	p.touch(t)
	p.Discs = appendCappedTime(p.Discs, t)
}

// AddMessage records one accepted chat message (time only).
func (p *Profile) AddMessage(t time.Time) {
	p.touch(t)
	p.Msgs = appendCappedTime(p.Msgs, t)
	p.TotalMsgs++
}

// AddHistoryReq records one on-demand history request (time only).
func (p *Profile) AddHistoryReq(t time.Time) {
	p.touch(t)
	p.HistReq = appendCappedTime(p.HistReq, t)
}

// AddHTTP records one non-chat request (class/status/time only).
func (p *Profile) AddHTTP(e HTTPEvent) {
	p.touch(e.Time)
	if len(p.HTTP) >= maxRing {
		p.HTTP = p.HTTP[len(p.HTTP)-maxRing+1:]
	}
	p.HTTP = append(p.HTTP, e)
}

// AddErr records one auth/protocol anomaly code (time only).
func (p *Profile) AddErr(e ErrEvent) {
	p.touch(e.Time)
	if len(p.Errs) >= maxRing {
		p.Errs = p.Errs[len(p.Errs)-maxRing+1:]
	}
	p.Errs = append(p.Errs, e)
}

// SetIdentity keeps the strongest identity kind ever seen.
func (p *Profile) SetIdentity(k IdentityKind) {
	if k > p.Identity {
		p.Identity = k
	}
}

// SetBlocklisted pins the operator blocklist hit.
func (p *Profile) SetBlocklisted() { p.Blocklisted = true }
