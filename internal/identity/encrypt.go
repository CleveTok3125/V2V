package identity

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

type encryptEnvelope struct {
	Version int    `json:"version"`
	Encrypted struct {
		KDF        string `json:"kdf"`
		Time       uint32 `json:"t"`
		Memory     uint32 `json:"m"`
		Threads    uint8  `json:"p"`
		Salt       string `json:"salt"`
		Nonce      string `json:"nonce"`
		Cipher     string `json:"cipher"`
		Ciphertext string `json:"ciphertext"`
	} `json:"encrypted"`
}

// Presets
var (
	PresetWASM   = Params{Time: 1, Memory: 32 * 1024, Threads: 1}
	PresetNative = Params{Time: 3, Memory: 64 * 1024, Threads: 4}
)

type Params struct {
	Time    uint32
	Memory  uint32
	Threads uint8
}

func defaultParams() Params {
	// wasm has single thread, use WASM preset
	return PresetWASM
}

func encryptJSON(plain, passphrase []byte, p Params) ([]byte, error) {
	if len(passphrase) == 0 {
		return nil, errors.New("empty passphrase")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	key := argon2.IDKey(passphrase, salt, p.Time, p.Memory, p.Threads, chacha20poly1305.KeySize)
	defer ZeroBytes(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nil, nonce, plain, nil)
	env := encryptEnvelope{Version: 3}
	env.Encrypted.KDF = "argon2id"
	env.Encrypted.Time = p.Time
	env.Encrypted.Memory = p.Memory
	env.Encrypted.Threads = p.Threads
	env.Encrypted.Salt = base64.RawURLEncoding.EncodeToString(salt)
	env.Encrypted.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
	env.Encrypted.Cipher = "xchacha20poly1305"
	env.Encrypted.Ciphertext = base64.RawURLEncoding.EncodeToString(ciphertext)
	return json.MarshalIndent(env, "", "  ")
}

func decryptJSON(data, passphrase []byte) ([]byte, error) {
	var env encryptEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Version != 3 || env.Encrypted.KDF != "argon2id" || env.Encrypted.Cipher != "xchacha20poly1305" {
		return nil, errors.New("unsupported envelope (want v3 argon2id+xchacha20poly1305)")
	}
	if env.Encrypted.Ciphertext == "" {
		return nil, errors.New("not encrypted")
	}
	salt, err := base64.RawURLEncoding.DecodeString(env.Encrypted.Salt)
	if err != nil {
		return nil, fmt.Errorf("bad salt: %w", err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(env.Encrypted.Nonce)
	if err != nil {
		return nil, fmt.Errorf("bad nonce: %w", err)
	}
	ct, err := base64.RawURLEncoding.DecodeString(env.Encrypted.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("bad ciphertext: %w", err)
	}
	p := Params{Time: env.Encrypted.Time, Memory: env.Encrypted.Memory, Threads: env.Encrypted.Threads}
	if p.Time == 0 {
		p = defaultParams()
	}
	// Clamp file-supplied cost: a crafted envelope must not trigger
	// multi-GB allocation or an argon2 thread panic. Memory is KiB.
	if p.Time < 1 || p.Time > 10 || p.Memory < 8*1024 || p.Memory > 256*1024 || p.Threads < 1 || p.Threads > 8 {
		return nil, errors.New("argon2 parameters out of range")
	}
	key := argon2.IDKey(passphrase, salt, p.Time, p.Memory, p.Threads, chacha20poly1305.KeySize)
	defer ZeroBytes(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("incorrect passphrase or corrupted file")
	}
	return plain, nil
}

// EncryptData seals an arbitrary JSON payload in the version 3 envelope
// (argon2id + XChaCha20Poly1305), the same construction key files use.
// Exported so sibling secrets (e.g. the client tripcode file) share one
// audited envelope instead of reinventing it.
func EncryptData(plain, passphrase []byte) ([]byte, error) {
	return encryptJSON(plain, passphrase, defaultParams())
}

// DecryptData opens a version 3 envelope. Wrong passphrase and corrupt
// files both fail closed.
func DecryptData(data, passphrase []byte) ([]byte, error) {
	return decryptJSON(data, passphrase)
}

// IsEncryptedData reports whether raw bytes look like a version 3 envelope.
func IsEncryptedData(data []byte) bool {
	return isEncrypted(data)
}

// AtomicWriteFile durably writes data with owner-only permissions via
// temp-file + fsync + rename. Exported for sibling secret files.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return atomicWriteFile(path, data, perm)
}

func isEncrypted(data []byte) bool {
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	_, ok := probe["encrypted"]
	return ok
}

// ZeroBytes wipes a secret buffer in place. Callers convert their
// secret once, defer this, and drop the reference on return.
func ZeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
