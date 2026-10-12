<!-- Body of the next release: release.yml passes this file to goreleaser with
--release-notes, which uses it verbatim instead of the generated commit list.
Replace the whole content when cutting a release; older bodies live on GitHub. -->

## v0.10.0

A month of work since v0.9.0. The stack gains passkey identities, a pairing
flow for desktop clients, on-demand history, and a documented standard for
what a log line looks like.

### Identities and pairing

- **Passkey identities for roles.** WebAuthn credentials replace the soft key
  slots: enrollment runs through an admin ticket, and the login counter is
  verified against the managed store. Ed25519 key files, tripcodes and guest
  logins are unchanged.
- **Desktop pairing.** `v2v --passkey <role>` shows a link as a scannable code.
  The browser proves the ticket and the client collects the result, so the chat
  socket is never held open through a ceremony; the link is wiped as soon as a
  browser proves it.
- **One identity file.** `identity.json` carries one Ed25519 key and one
  passkey slot, and the desktop client can mint a software passkey.

### Client

- **Message layout.** One message is one buffer entry: the author sits above
  the body, a quote rides inside the entry it answers, and entries are set
  apart by a blank row.
- **Input.** The submitted line becomes a buffer entry the session owns, so the
  echo, the pending placeholder and the server echo replace one another instead
  of stacking.
- **History on demand.** `/older [n]` pages below the oldest height in memory
  over the authenticated socket; the client asks for the exact heights it is
  missing and merges the refill in place.
- **Rendering.** Chroma-highlighted code blocks, OSC 8 hyperlinks, mentions,
  `/reply` quotes, a `/find` that searches inside collapsed blocks, and
  `/info <height>` for the full metadata of one message, re-verified locally.
- **Config.** `config.jsonc` with comments, a passphrase-sealed variant,
  hot-reloadable settings, and a pre-dial version check with a configurable
  policy.

### Server

- **History.** On-demand segments over the socket, tiered disk lookup, a per-IP
  cost budget, and a `seq` cursor with range refills for missed heights.
- **Ingress.** An explicit trusted-proxy chain with per-module trust files,
  onion ingress with dedicated hop trust, a blocklist, an IPv4-only switch and a
  global connection cap.
- **Abuse.** An under-attack sampler, a join gate (proof of work, a wait, and
  reusable or single-use passes), a behavior engine that scores addresses by
  ASN and region, and `Retry-After` on every metered endpoint.
- **Privacy.** `NO_CONTENT_LOGS` keeps message content out of the logs and off
  the disk, and warns about artifacts a previous run left behind.

### Web client

- A settings panel, a human-readable trip verify page showing every field, a
  passkey login bridge and enrollment UI, and a confirmation before an external
  link or the verify page opens.

### Tooling

- `v2vctl` grows instances (`instance init/list/status/up/down/restart/logs`),
  manifest-driven `config sync`, a `config take` picker for local-versus-
  template drift, command aliases with short flags, and role management split
  from keygen.

### Standards

- **Log lines** open with `[LEVEL] <emoji> (<tag>[,<tag>]) <message>`. The tags
  are independent labels, the vocabulary is declared in `server/logtags.go`, and
  the rules are in [LOG_STANDARD.md](LOG_STANDARD.md). A boot failure is an
  `ERROR` line carrying the `fatal` tag.
- **Display names** are validated against an allowlist: letters, decimal
  digits, spacing combining marks, and the separators ` `, `_`, `-`, `.`, `'`.
  The rule only ever refuses more than the one it replaced.
- **Notice tags** ([NOTICE_TAGS.md](NOTICE_TAGS.md)) decide which tab a notice
  lands in and which switch mutes it.
- **History sync** ([HISTORY_SYNC.md](HISTORY_SYNC.md)) documents the segment,
  range and budget protocol.

## Compatibility

| Change | Action |
| --- | --- |
| Notice tags replace `sys_kind` | none; there is no compatibility with the old field |
| Passkey store rebuilt (v1 store, `key.json` soft slots, `roles.json` `passkeys[]`) | re-enroll every passkey |
| Instance layout (`instances/<name>/.env` plus `config/`) | migrate the root layout, or keep it with `V2V_ROOT=.` and `config sync --dir . --to .` |
| `v2v://` links | removed; only `https` links are handled |
| Top-level `trip` field in stored records | removed; only `wire.trip` is kept |
| Legacy history records | migrated in place by the `seq` backfill |
| Display-name allowlist | a name outside it becomes `Anonymous` |
| Log line format | update anything that greps the log |
| `HISTORY_SEGMENT_COOLDOWN` | replaced by `HISTORY_BUDGET_BURST` and `HISTORY_BUDGET_PER_SEC` |

## Upgrading

```sh
./v2vctl config sync --dir . --to instances/default   # merge the bundled template
```

The merge is template-first: the template wins structure, order and comments,
the live config wins values per key. A key that is new arrives with its
documented default, and a value an operator set is preserved.

## Artifacts

| Artifact | Purpose |
| --- | --- |
| `V2V-<os>-<arch>[.exe]` | Chat client |
| `V2Vctl-<os>-<arch>[.exe]` | Admin tool (roles, keys, enrollment, instances) |
| `V2V-server-<os>-<arch>.tar.gz` / `.zip` | Self-contained server bundle |
| `V2V-webterm-<version>.zip` | Standalone browser client (wasm and `serve.py`) |
| `ghcr.io/clevetok3125/v2v:<version>` | Multi-arch container image (`linux/amd64`, `linux/arm64`) |
| `SHA256SUMS`, `SHA256SUMS.sigstore.json` | Checksums and a cosign keyless signature bundle |

The server bundle carries the server binary, a matching `v2vctl`, `webterm/`,
`template/`, `v2v-template.json` and `docker-compose.yml`.

## Verifying a download

```sh
sha256sum -c SHA256SUMS --ignore-missing
cosign verify-blob \
  --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp 'https://github.com/CleveTok3125/V2V/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
```
