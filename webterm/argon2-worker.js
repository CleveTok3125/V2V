// Simple PBKDF2 fallback worker for tripcode derivation.
// The Go WASM main thread can offload Argon2-like KDF here to avoid blocking UI.
// For now, uses SubtleCrypto PBKDF2 (100k iterations) as fallback if argon2.wasm not available.
// Message format: {passphrase: string, salt: string (hex)}
// Response: {keyHex: string (64 hex = 32 bytes)}

self.onmessage = async function (e) {
  const { passphrase, saltHex } = e.data;
  try {
    let salt = hexToBytes(saltHex);
    // Try to use Argon2 wasm if available, else PBKDF2
    let key;
    if (typeof self.argon2 !== "undefined") {
      // argon2.wasm expected to expose argon2.hash
      key = await self.argon2.hash({ pass: passphrase, salt: salt, time: 1, mem: 32768, parallelism: 1, hashLen: 32 });
    } else {
      // PBKDF2 fallback via SubtleCrypto
      const enc = new TextEncoder();
      const keyMaterial = await crypto.subtle.importKey("raw", enc.encode(passphrase), "PBKDF2", false, ["deriveBits"]);
      const bits = await crypto.subtle.deriveBits({ name: "PBKDF2", salt: salt, iterations: 100000, hash: "SHA-256" }, keyMaterial, 256);
      key = new Uint8Array(bits);
    }
    self.postMessage({ keyHex: bytesToHex(key) });
  } catch (err) {
    self.postMessage({ error: err.message });
  }
};

function hexToBytes(hex) {
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < bytes.length; i++) bytes[i] = parseInt(hex.substr(i * 2, 2), 16);
  return bytes;
}
function bytesToHex(bytes) {
  return Array.from(bytes).map(b => b.toString(16).padStart(2, "0")).join("");
}
