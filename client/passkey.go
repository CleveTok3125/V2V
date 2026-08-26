package main

// Web-side passkey plumbing: the assertion JSON shape exchanged with the
// page bridge. Identity/crypto logic lives in the shared identity package.

// webauthnAssertion mirrors the server's AuthPacket passkey fields.
type webauthnAssertion struct {
	PasskeyID  string `json:"passkey_id"`
	AuthData   string `json:"passkey_auth_data"`
	ClientData string `json:"passkey_client_data"`
	Sig        string `json:"passkey_sig"`
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
