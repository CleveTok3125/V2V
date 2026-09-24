package pow

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/argon2"
)

// vectorsPath is the shared cross-language vector file, also consumed
// by scripts/pow_vectors_test.mjs (JS side).
func vectorsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "web", "vectors.json")
}

type vectors struct {
	Argon2 struct {
		Passphrase string `json:"passphrase"`
		SaltHex    string `json:"saltHex"`
		T          uint32 `json:"t"`
		M          uint32 `json:"m"`
		P          uint8  `json:"p"`
		DKLen      uint32 `json:"dkLen"`
		KeyHex     string `json:"keyHex"`
	} `json:"argon2"`
	Pow struct {
		Domain     string `json:"domain"`
		Salt       string `json:"salt"`
		T          int    `json:"t"`
		M          int    `json:"m"`
		P          int    `json:"p"`
		Difficulty uint   `json:"difficulty"`
		Nonce      uint64 `json:"nonce"`
	} `json:"pow"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	data, err := os.ReadFile(vectorsPath(t))
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	return v
}

func TestVectorArgon2(t *testing.T) {
	v := loadVectors(t)
	salt, err := hex.DecodeString(v.Argon2.SaltHex)
	if err != nil {
		t.Fatal(err)
	}
	key := argon2.IDKey([]byte(v.Argon2.Passphrase), salt, v.Argon2.T, v.Argon2.M, v.Argon2.P, v.Argon2.DKLen)
	if got := hex.EncodeToString(key); got != v.Argon2.KeyHex {
		t.Fatalf("argon2 mismatch:\n got %s\nwant %s", got, v.Argon2.KeyHex)
	}
}

func TestVectorPow(t *testing.T) {
	v := loadVectors(t)
	p := Preset{Time: v.Pow.T, Memory: v.Pow.M, Threads: v.Pow.P, Difficulty: v.Pow.Difficulty}
	nonce, err := Solve(p, v.Pow.Salt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if nonce != v.Pow.Nonce {
		t.Fatalf("pow nonce mismatch: got %d want %d", nonce, v.Pow.Nonce)
	}
	// The solution must verify and the immediately preceding nonce must not.
	if !Verify(p, v.Pow.Salt, v.Pow.Nonce) {
		t.Fatal("vector nonce must verify")
	}
	if v.Pow.Nonce > 0 {
		key := DeriveKey(p, v.Pow.Salt)
		var nb [8]byte
		binary.LittleEndian.PutUint64(nb[:], v.Pow.Nonce-1)
		h := sha256.Sum256(append(append([]byte{}, key...), nb[:]...))
		if LeadingZeros(h) >= p.Difficulty {
			t.Fatal("previous nonce must not already satisfy difficulty")
		}
	}
	_ = v.Pow.Domain
}
