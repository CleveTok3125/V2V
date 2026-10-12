# Log Standard

Status: implemented. Every server log line opens with its level, an emoji and
its tags, so a line reads the same however its wording is edited and a grep
finds it by what happened rather than by how it was phrased.

## Line shape

```
[LEVEL] <emoji> (<tag>[,<tag>]) <message>
```

```
[WARN]  ⛔ (filter,reject) Alice#a1b2 (203.0.113.7): message contains invalid character
[INFO]  🟢 (session,join) Alice#a1b2 ◆a1b2c3d4 (IP: 203.0.113.7)
[ERROR] 🚨 (auth,fail) 203.0.113.7: Signature sai định dạng cho role [member]
[WARN]  ⚠️ (geoip) geoip: ./config/geoip missing; ASN/region grouping disabled
```

- `[LEVEL]` is `INFO`, `WARN` or `ERROR`, prepended by `server/loglevel.go`.
  A boot failure goes through `logFatalf`, which reports the line at `ERROR`
  and then exits, so no line is printed without a level.
- `<emoji>` is one visible marker immediately after the level.
- `(<tag>[,<tag>])` follows the emoji, one space either side, no space inside
  the parentheses.
- `<message>` is free text, in the language the operator reads.

## Levels and emoji

An outcome emoji means a level, and a line has to be logged at the level its
emoji means. `TestLogEmojiMatchesLevel` holds this over the call sites.

| Emoji | Level | Means |
| --- | --- | --- |
| `✅` | INFO | the attempt succeeded |
| `🟢` | INFO | a session arrived |
| `⚠️` | WARN | degraded, carrying on |
| `⛔` | WARN | refused |
| `❌` | ERROR | an operation failed |
| `🚨` | ERROR | a security alert |

Emoji that mark the *kind* of event rather than its severity — `🔴` leave,
`🛑` shutdown, `🔄` reload, `🔌` request, `🔐`/`🔍`/`🎫`/`🔗` ceremony steps,
`📚` history, `🔑` identity, `🌍` geoip, `🧅` onion, `🛡️` proxy, `🚀` boot,
`⏱️` idle timeout — may appear at whatever level the event is worth.

## Tags

The tags are independent labels, not a path. Each one is a fact about the line,
and a line carries every fact that applies, comma-separated and never repeated.
`auth,fail` says the line is about authentication and that it failed;
`passkey,fail` says the same about a passkey, and `fail` is the same tag in
both, so one grep finds every line that carries it. Order is not significant.

A tag is lowercase ASCII: `[a-z0-9_]`. A new tag is a deliberate addition to the
catalog, never a spelling invented at a call site.

| Tag | Marks |
| --- | --- |
| `alert` | an attempt that looks like an attack |
| `auth` | authentication and authorization |
| `ban` | an address banned or refused for its rate |
| `begin` | a ceremony or flow started |
| `behavior` | the behavior engine scored, saved or failed |
| `bind` | a pairing ticket was bound to a challenge |
| `blocklist` | the blocklist matched or was loaded |
| `boot` | the server started listening |
| `breach` | a check that must never fail failed |
| `brute_force` | a source crossed the brute-force threshold |
| `chain` | the stored trip chain |
| `concurrent` | a second writer touched the same state |
| `config` | configuration was loaded, or a value fell back |
| `create_failed` | a record could not be created |
| `created` | a record was created |
| `enroll` | passkey enrollment |
| `fail` | an attempt or an operation failed |
| `fatal` | the process is about to exit |
| `filter` | a display name, a prefix or a message was refused |
| `finish` | a ceremony or flow was submitted |
| `fork` | the chain forked |
| `gap` | the chain has a gap |
| `geoip` | the GeoIP databases were loaded, or are missing |
| `history` | history was loaded, restored, flushed or dropped |
| `identity` | the server identity file |
| `idle_timeout` | a session was dropped for not chatting |
| `ipv6` | an address was refused because IPv4 is required |
| `join` | a session joined |
| `leave` | a session left |
| `limit` | an address was refused by a connection limit |
| `network` | address handling |
| `onion` | onion ingress or trust |
| `page` | the trip verify page |
| `pair` | passkey pairing |
| `passkey` | a passkey ceremony |
| `proxy` | trusted-proxy resolution |
| `rate` | a rate limit |
| `reject` | a request was refused |
| `reload` | the hot-reloadable configuration was reloaded |
| `replay` | a replay request was refused |
| `request` | an HTTP request |
| `security` | a security-relevant state |
| `session` | a chat session |
| `shutdown` | the server received a stop signal |
| `store` | a record reached its store |
| `submit` | a ticket or a proof was submitted |
| `success` | an attempt succeeded |
| `tamper` | stored data did not verify |
| `ticket` | a pairing ticket |
| `under_attack` | the under-attack state |
| `verify` | the trip verify surface |
| `web` | the web client |

## Values a line forwards

A line that forwards a warning built elsewhere carries the marker itself: the
text a loader returns is plain, and the emoji and the tags belong to the line. A
warning string that brought its own marker would print twice, so
`internal/serverconfig` and `server/blocklist.go` return plain text;
`v2vctl config validate` prints the same strings and prefixes its own marker,
and `logGeoipWarnings` adds the marker at the call site.

## Attacker-controlled values

A value taken from a request is clipped before it reaches a line
(`trustedproxy.Clip`), so one request stays one line. With
`NO_CONTENT_LOGS=true` message content is never logged, and rejected payloads
are recorded by error type only.

## Catalog

The vocabulary lives in `server/logtags.go`. `TestLogCallSitesUseDeclaredTags`
fails when a call site writes a tag the catalog does not declare, or opens
without the `<emoji> (<tag>) ` shape, or repeats a tag; it also fails when a
declared tag is used by no line. `TestLogTagsAreDocumented` keeps the catalog
and this table in step in both directions.
