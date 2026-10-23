# Independent verification

Every verdict this chat shows can be rechecked without the client, the server, or any built-in command — only standard crypto (SHA-256, Ed25519) and the values already on screen or in history. All hex is lowercase; integers are decimal unless noted.

Inputs live in three places: badge verify links carry `pub, seq, prev, sig, msg_hash, server_pub, display_name, tmp_id, reply_to` (plus display-only `height, sent_at`); `/info` lists the same fields; full wires (with `time`, `chain_*`) come from the server's `history.jsonl`.

## 1. Message content

`msg_hash` is SHA-256 over the raw message text:

```python
import hashlib
assert hashlib.sha256(text.encode()).hexdigest() == msg_hash
```

## 2. Trip signature

The signed payload is eight `\x00`-separated fields — `server_pub, seq, prev, msg_hash, pub, display_name, tmp_id, reply_to` — where byte fields render as lowercase hex:

```python
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
payload = (server_pub + "\x00" + str(seq) + "\x00" + prev + "\x00" + msg_hash
           + "\x00" + pub + "\x00" + display_name + "\x00" + str(tmp_id)
           + "\x00" + str(reply_to)).encode()
Ed25519PublicKey.from_public_bytes(bytes.fromhex(pub)).verify(bytes.fromhex(sig), payload)
```

When `tmp_id` and `reply_to` are both `0`, the last two fields are absent (legacy encoding, kept so old history and old links still check). A `prev` of 64 zeros starts a new chain.

## 3. Badge

The badge names the pub, not the person: `◆` plus the first 8 hex of SHA-256 over the decoded pub bytes.

```python
assert badge == "◆ " + hashlib.sha256(bytes.fromhex(pub)).hexdigest()[:8]
```

## 4. Chain link

Each stored wire links the previous one. The hash covers a domain tag, then length-prefixed fields (`prev` is 32 raw bytes; integers are unsigned varints):

- domain: `V2V-chain-v1` + `\x00`
- `prev`, `height`, `tmp_id`, `reply_to`
- `type`, `time`, `display_name`, `text`, trip `sig` (empty for untripped wires), each as varint length + bytes

```python
def uvarint(n):
    out = bytearray()
    while True:
        b, n = (n & 0x7F) | (0x80 if n > 0x7F else 0), n >> 7
        out.append(b)
        if not n:
            break
    return bytes(out)

h = hashlib.sha256()
h.update(b"V2V-chain-v1\x00")
h.update(uvarint(32) + bytes.fromhex(chain_prev))
h.update(uvarint(height) + uvarint(tmp_id) + uvarint(reply_to))
for s in (type_, time_, display_name, text, trip_sig):
    e = s.encode()
    h.update(uvarint(len(e)) + e)
assert h.hexdigest() == chain_hash
```

Walk backwards: each wire's `chain_prev` must equal the previous wire's `chain_hash`, down to the genesis `sha256("V2V-chain-genesis\x00" + server_pub)`. Wires without `chain_ver` use the same layout minus `reply_to`. System notices are unchained by design and carry no proof of order.

## 5. Seed reveal

A reveal ends its identity: anyone holding the seed can sign as the badge. The room notice carries `exposed_pub`, `exposed_sig`, and the published `exposed_seed` itself, so the proof is checkable the same way:

```python
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
seed = bytes.fromhex(exposed_seed)
assert Ed25519PrivateKey.from_private_bytes(seed).public_key().public_bytes_raw().hex() == exposed_pub
payload = b"v2v.trip.reveal.v1\x00" + exposed_pub.encode()
Ed25519PublicKey.from_public_bytes(bytes.fromhex(exposed_pub)).verify(bytes.fromhex(exposed_sig), payload)
```

The reveal stays in room history after the server's 24h flag lapses, so old proof remains checkable. Revealing is permanent: the seed persists in history, logs, and backups.
