package main

// logTag is one entry of the log vocabulary: a tag a line may carry, and what
// it marks.
type logTag struct {
	tag   string
	about string
}

// logTagCatalog is the vocabulary of log tags, in the order
// docs/LOG_STANDARD.md tabulates them. A line reports its event as
//
//	[LEVEL] <emoji> (<tag>[,<tag>]) <message>
//
// The tags are independent labels, not a path: each one is a fact about the
// line, and a line carries every fact that applies. "auth,fail" says the line
// is about authentication and that it failed; "passkey,fail" says the same
// about a passkey, and "fail" is the same tag in both, so a grep for one fact
// finds every line that carries it. Order is not significant and a tag is
// never repeated.
//
// TestLogCallSitesUseDeclaredTags keeps every call site in step with this list
// and TestLogTagsAreDocumented keeps the list in step with the spec, in both
// directions, so a new tag is a deliberate addition and never a typo that only
// shows up in production.
var logTagCatalog = []logTag{
	{"alert", "an attempt that looks like an attack"},
	{"auth", "authentication and authorization"},
	{"ban", "an address banned or refused for its rate"},
	{"begin", "a ceremony or flow started"},
	{"behavior", "the behavior engine scored, saved or failed"},
	{"bind", "a pairing ticket was bound to a challenge"},
	{"blocklist", "the blocklist matched or was loaded"},
	{"boot", "the server started listening"},
	{"breach", "a check that must never fail failed"},
	{"brute_force", "a source crossed the brute-force threshold"},
	{"chain", "the stored trip chain"},
	{"concurrent", "a second writer touched the same state"},
	{"config", "configuration was loaded, or a value fell back"},
	{"create_failed", "a record could not be created"},
	{"created", "a record was created"},
	{"enroll", "passkey enrollment"},
	{"fail", "an attempt or an operation failed"},
	{"filter", "a display name, a prefix or a message was refused"},
	{"finish", "a ceremony or flow was submitted"},
	{"fork", "the chain forked"},
	{"gap", "the chain has a gap"},
	{"geoip", "the GeoIP databases were loaded, or are missing"},
	{"history", "history was loaded, restored, flushed or dropped"},
	{"identity", "the server identity file"},
	{"idle_timeout", "a session was dropped for not chatting"},
	{"ipv6", "an address was refused because IPv4 is required"},
	{"join", "a session joined"},
	{"leave", "a session left"},
	{"limit", "an address was refused by a connection limit"},
	{"network", "address handling"},
	{"onion", "onion ingress or trust"},
	{"page", "the trip verify page"},
	{"pair", "passkey pairing"},
	{"passkey", "a passkey ceremony"},
	{"proxy", "trusted-proxy resolution"},
	{"rate", "a rate limit"},
	{"reject", "a request was refused"},
	{"reload", "the hot-reloadable configuration was reloaded"},
	{"replay", "a replay request was refused"},
	{"request", "an HTTP request"},
	{"security", "a security-relevant state"},
	{"session", "a chat session"},
	{"shutdown", "the server received a stop signal"},
	{"store", "a record reached its store"},
	{"submit", "a ticket or a proof was submitted"},
	{"success", "an attempt succeeded"},
	{"tamper", "stored data did not verify"},
	{"ticket", "a pairing ticket"},
	{"under_attack", "the under-attack state"},
	{"verify", "the trip verify surface"},
	{"web", "the web client"},
}

// hasLogTag reports whether tag is a declared tag.
func hasLogTag(tag string) bool {
	for _, t := range logTagCatalog {
		if t.tag == tag {
			return true
		}
	}
	return false
}
