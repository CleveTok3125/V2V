package main

import (
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/identity"
)

func TestPickIdentityFrom(t *testing.T) {
	pick := func(line string, eof bool) (bool, bool) {
		return pickIdentityFrom(strings.NewReader(line+"\n"), &IdentityFile{
			Version: identity.Version,
			Ed25519: &Ed25519Identity{}, Passkey: &PasskeyIdentity{},
		})
	}
	// EOF default prefers passkey
	if ed, pk := pick("", true); ed || !pk {
		t.Errorf("EOF default should prefer passkey, got ed=%v pk=%v", ed, pk)
	}
	if ed, pk := pick("1", false); !ed || pk {
		t.Errorf("menu '1' should pick ed25519, got ed=%v pk=%v", ed, pk)
	}
	if _, pk := pick("2", false); !pk {
		t.Errorf("menu '2' should pick passkey")
	}
}
