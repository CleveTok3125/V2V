package identity

import "testing"


func TestDataEnvelopeRoundtrip(t *testing.T) {
	plain := []byte(`{"tripcode":"s3cret"}`)
	enc, err := EncryptData(plain, "unlock")
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncryptedData(enc) {
		t.Fatal("must detect envelope")
	}
	if IsEncryptedData(plain) {
		t.Fatal("plain must not detect as envelope")
	}
	back, err := DecryptData(enc, "unlock")
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != string(plain) {
		t.Fatal("roundtrip mismatch")
	}
	if _, err := DecryptData(enc, "wrong"); err == nil {
		t.Fatal("wrong passphrase must fail")
	}
	if _, err := EncryptData(plain, ""); err == nil {
		t.Fatal("empty passphrase must fail")
	}
}
