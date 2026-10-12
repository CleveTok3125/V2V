package pow

import (
	"crypto/ed25519"
	"fmt"
	"time"
)

// OfferDomain separates PoW offers from auth_challenge signatures.
const OfferDomain = "V2V-POW-v1"

// Offer is a server-issued PoW challenge: the tier, the exact preset,
// the bound salt and an expiry. The client must verify the server
// signature against the pinned serverPub before spending any work;
// tampering with tier or difficulty breaks the signature.
type Offer struct {
	Tier        int
	Preset      Preset
	Salt        string
	Expires     int64
	ChallengeID string
}

// Expired reports whether the offer is past its deadline at now.
func (o Offer) Expired(now time.Time) bool {
	return !now.Before(time.Unix(o.Expires, 0))
}

func offerMessage(o Offer) []byte {
	return []byte(fmt.Sprintf("%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%s\x00%d\x00%s",
		OfferDomain, o.Tier, o.Preset.Time, o.Preset.Memory,
		o.Preset.Threads, o.Preset.Difficulty, o.Salt, o.Expires, o.ChallengeID))
}

// SignOffer signs the offer with the server private key.
func SignOffer(priv ed25519.PrivateKey, o Offer) []byte {
	return ed25519.Sign(priv, offerMessage(o))
}

// VerifyOffer checks the offer signature against the server public key.
func VerifyOffer(pub ed25519.PublicKey, o Offer, sig []byte) bool {
	return ed25519.Verify(pub, offerMessage(o), sig)
}
