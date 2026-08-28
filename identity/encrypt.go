package identity

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

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

func encryptJSON(plain []byte, passphrase string, p Params) ([]byte, error) {
	if passphrase == "" {
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
	key := argon2.IDKey([]byte(passphrase), salt, p.Time, p.Memory, p.Threads, chacha20poly1305.KeySize)
	defer zeroBytes(key)
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

func decryptJSON(data []byte, passphrase string) ([]byte, error) {
	var env encryptEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
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
	key := argon2.IDKey([]byte(passphrase), salt, p.Time, p.Memory, p.Threads, chacha20poly1305.KeySize)
	defer zeroBytes(key)
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

func isEncrypted(data []byte) bool {
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	_, ok := probe["encrypted"]
	return ok
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
