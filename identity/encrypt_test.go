package identity

import "testing"


func TestDataEnvelopeRoundtrip(t *testing.T) {
	plain := []byte(`{"tripcode":"s3cret"}`)
	enc, err := EncryptData(plain, []byte("unlock"))
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncryptedData(enc) {
		t.Fatal("must detect envelope")
	}
	if IsEncryptedData(plain) {
		t.Fatal("plain must not detect as envelope")
	}
	back, err := DecryptData(enc, []byte("unlock"))
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != string(plain) {
		t.Fatal("roundtrip mismatch")
	}
	if _, err := DecryptData(enc, []byte("wrong")); err == nil {
		t.Fatal("wrong passphrase must fail")
	}
	if _, err := EncryptData(plain, nil); err == nil {
		t.Fatal("empty passphrase must fail")
	}
}

func TestZeroBytes(t *testing.T) {
	b := []byte("secret")
	ZeroBytes(b)
	for i, v := range b {
		if v != 0 {
			t.Fatalf("byte %d not zeroed: %d", i, v)
		}
	}
	ZeroBytes(nil) // must not panic
}

func TestDecryptEvilParamsRejected(t *testing.T) {
	// Crafted cost parameters must fail closed, never reach argon2.
	evil := `{"version":3,"encrypted":{"kdf":"argon2id","t":255,"m":4294967295,"p":255,` +
		`"salt":"AAAAAAAAAAAAAAAAAAAAAA","nonce":"AAAAAAAAAAAAAAAAAAAAAAAAAAA","cipher":"xchacha20poly1305",` +
		`"ciphertext":"AA"}}`
	if _, err := DecryptData([]byte(evil), []byte("x")); err == nil {
		t.Fatal("evil argon2 params accepted")
	}
	wrongKDF := `{"version":3,"encrypted":{"kdf":"scrypt","t":1,"m":32768,"p":1,` +
		`"salt":"AAAAAAAAAAAAAAAAAAAAAA","nonce":"AAAAAAAAAAAAAAAAAAAAAAAAAAA","cipher":"xchacha20poly1305",` +
		`"ciphertext":"AA"}}`
	if _, err := DecryptData([]byte(wrongKDF), []byte("x")); err == nil {
		t.Fatal("non-argon2id envelope accepted")
	}
}
