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
