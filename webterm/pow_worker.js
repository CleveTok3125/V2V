"use strict";
// PoW + argon2 Web Worker. Runs the CPU-heavy crypto off the main
// thread so the page (and the Go WASM client) never freeze. Uses the
// vendored @noble/hashes bundle for argon2id and sha256; the password
// for PoW is the fixed domain string, matching internal/pow.
// Same domain separator as internal/pow.DeriveKey.
const POW_DOMAIN = "V2V-pow-v1";
const nobleURL = "/web/vendor/noble.js";
let noblePromise = null;
function noble() {
    if (!noblePromise) {
        // Non-literal specifier: resolved at runtime, not at TS compile time.
        noblePromise = import(/* @vite-ignore */ nobleURL);
    }
    return noblePromise;
}
function hexToBytes(hex) {
    const out = new Uint8Array(hex.length / 2);
    for (let i = 0; i < out.length; i++) {
        out[i] = parseInt(hex.substr(i * 2, 2), 16);
    }
    return out;
}
function bytesToHex(b) {
    let s = "";
    for (let i = 0; i < b.length; i++)
        s += b[i].toString(16).padStart(2, "0");
    return s;
}
function leadingZeros(h) {
    let n = 0;
    for (let i = 0; i < h.length; i++) {
        const b = h[i];
        if (b === 0) {
            n += 8;
            continue;
        }
        for (let bit = 7; bit >= 0; bit--) {
            if (b & (1 << bit))
                return n;
            n++;
        }
        return n;
    }
    return n;
}
function candidate(key, nonce) {
    const buf = new Uint8Array(key.length + 8);
    buf.set(key, 0);
    let v = BigInt(nonce);
    for (let i = 0; i < 8; i++) {
        buf[key.length + i] = Number(v & 0xffn);
        v >>= 8n;
    }
    return buf;
}
async function runPow(msg) {
    const { argon2id, sha256 } = await noble();
    const enc = new TextEncoder();
    const key = argon2id(enc.encode(POW_DOMAIN), enc.encode(msg.salt), {
        t: msg.t,
        m: msg.m,
        p: msg.p,
        dkLen: 32,
    });
    const deadline = msg.maxMs > 0 ? Date.now() + msg.maxMs : 0;
    for (let nonce = 0;; nonce++) {
        if ((nonce & 0x3ff) === 0 && deadline && Date.now() > deadline) {
            throw new Error("pow budget exceeded");
        }
        if (leadingZeros(sha256(candidate(key, nonce))) >= msg.difficulty) {
            return nonce;
        }
    }
}
async function runArgon2(msg) {
    const { argon2id } = await noble();
    const key = argon2id(hexToBytes(msg.passHex), hexToBytes(msg.saltHex), {
        t: msg.t,
        m: msg.m,
        p: msg.p,
        dkLen: msg.dkLen,
    });
    return bytesToHex(key);
}
self.onmessage = (ev) => {
    const msg = ev.data;
    const reply = (result) => self.postMessage({ id: msg.id, ok: true, result });
    const fail = (err) => self.postMessage({ id: msg.id, ok: false, error: String(err && err.message ? err.message : err) });
    if (msg.op === "pow") {
        runPow(msg).then(reply, fail);
    }
    else {
        runArgon2(msg).then(reply, fail);
    }
};
