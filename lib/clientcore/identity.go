package clientcore

// Login-side glue over the shared identity package: encrypted key.json
// unlock, with the prompt and the strength note supplied by the caller.

import (
	"github.com/CleveTok3125/V2V/lib/env"
	"github.com/CleveTok3125/V2V/lib/identity"
)

// LoadIdentityFile reads key.json, unlocking an encrypted file with
// V2V_PASSPHRASE or, when that is empty, through the caller's Prompter.
//
// It returns the passphrase it unlocked with, nil for a plaintext file, so
// the caller can keep it and re-encrypt later saves; the caller owns that
// byte slice and must wipe it. warn, when not nil, receives the env
// passphrase so the caller can apply its own strength policy — refusing here
// would lock out legitimate files, so it is a note, not a gate.
func LoadIdentityFile(path string, prompter Prompter, warn func(passphrase string)) (*identity.IdentityFile, []byte, error) {
	if enc, _ := identity.IsEncrypted(path); enc {
		if pass := env.Passphrase(); pass != "" {
			if warn != nil {
				warn(pass)
			}
			pw := []byte(pass)
			idf, err := identity.LoadEncrypted(path, pw)
			if err != nil {
				identity.ZeroBytes(pw)
				return nil, nil, err
			}
			return idf, pw, nil
		}
		pass, err := prompter.Secret(ReasonKeyFilePassphrase)
		if err != nil {
			return nil, nil, err
		}
		pw := []byte(pass)
		pass = "" // drop the string copy; the bytes are what we keep
		idf, err := identity.LoadEncrypted(path, pw)
		if err != nil {
			identity.ZeroBytes(pw)
			return nil, nil, err
		}
		return idf, pw, nil
	}
	idf, err := identity.Load(path)
	return idf, nil, err
}
