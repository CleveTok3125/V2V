// Cross-language parity test: read web/vectors.json and assert the
// vendored @noble/hashes bundle produces the same argon2id key and PoW
// nonce as Go (internal/pow). Run with `make test-web`.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const vectors = JSON.parse(readFileSync(join(here, "..", "web", "vectors.json"), "utf8"));
const { argon2id, sha256 } = await import(join(here, "..", "webterm", "vendor", "noble.js"));

function hexToBytes(hex) {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.substr(i * 2, 2), 16);
  return out;
}
function bytesToHex(b) {
  let s = "";
  for (const x of b) s += x.toString(16).padStart(2, "0");
  return s;
}
function leadingZeros(h) {
  let n = 0;
  for (const b of h) {
    if (b === 0) {
      n += 8;
      continue;
    }
    for (let i = 7; i >= 0; i--) {
      if (b & (1 << i)) return n;
      n++;
    }
    return n;
  }
  return n;
}

let failed = 0;
function check(name, got, want) {
  if (String(got) !== String(want)) {
    console.error(`FAIL ${name}: got ${got} want ${want}`);
    failed++;
  } else {
    console.log(`ok   ${name}`);
  }
}

const a = vectors.argon2;
const key = argon2id(new TextEncoder().encode(a.passphrase), hexToBytes(a.saltHex), {
  t: a.t,
  m: a.m,
  p: a.p,
  dkLen: a.dkLen,
});
check("argon2 keyHex", bytesToHex(key), a.keyHex);

const p = vectors.pow;
const powKey = argon2id(new TextEncoder().encode(p.domain), new TextEncoder().encode(p.salt), {
  t: p.t,
  m: p.m,
  p: p.p,
  dkLen: 32,
});
function candidate(nonce) {
  const buf = new Uint8Array(powKey.length + 8);
  buf.set(powKey, 0);
  let v = BigInt(nonce);
  for (let i = 0; i < 8; i++) {
    buf[powKey.length + i] = Number(v & 0xffn);
    v >>= 8n;
  }
  return buf;
}
let nonce = -1;
for (let n = 0; ; n++) {
  if (leadingZeros(sha256(candidate(n))) >= p.difficulty) {
    nonce = n;
    break;
  }
}
check("pow nonce", nonce, p.nonce);

if (failed > 0) {
  console.error(`${failed} vector(s) failed`);
  process.exit(1);
}
console.log("all pow/argon2 vectors match");
