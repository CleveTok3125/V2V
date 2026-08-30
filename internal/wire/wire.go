package wire

type Permission struct {
	CanMessageUnlimited bool   `json:"can_message_unlimited"`
	CustomPrefix        string `json:"custom_prefix"`
}

type TripMeta struct {
	Pub         string `json:"pub"`
	Seq         uint32 `json:"seq"`
	Prev        string `json:"prev"`
	Sig         string `json:"sig"`
	ServerPub   string `json:"server_pub"`
	MsgHash     string `json:"msg_hash,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type WireMessage struct {
	Type        string    `json:"type"`
	Time        string    `json:"time,omitempty"`
	DisplayName string    `json:"displayName,omitempty"`
	Text        string    `json:"text,omitempty"`
	Trip        *TripMeta `json:"trip,omitempty"`
}

type AuthPacket struct {
	Type      string `json:"type"`
	Nonce     string `json:"nonce,omitempty"`
	Role      string `json:"role,omitempty"`
	Signature string `json:"signature,omitempty"`
	Hmac      string `json:"hmac,omitempty"`
	Username  string `json:"username,omitempty"`
	Tripcode  string `json:"tripcode,omitempty"`
	TripPub   string `json:"trip_pub,omitempty"`
	PasskeyID         string `json:"passkey_id,omitempty"`
	PasskeyAuthData   string `json:"passkey_auth_data,omitempty"`
	PasskeyClientData string `json:"passkey_client_data,omitempty"`
	PasskeySig        string `json:"passkey_sig,omitempty"`
	ServerPubKey string `json:"server_pubkey,omitempty"`
	ServerSig    string `json:"server_sig,omitempty"`
	ServerHost   string `json:"server_host,omitempty"`
	Error       string `json:"error,omitempty"`
	IdentityPub string `json:"-"`
	AuthType string      `json:"auth_type,omitempty"`
	Perms    *Permission `json:"perms,omitempty"`
	TripSeq  uint32 `json:"trip_seq,omitempty"`
	TripPrev string `json:"trip_prev,omitempty"`
}
