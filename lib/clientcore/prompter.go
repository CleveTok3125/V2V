// Package clientcore holds the parts of a chat client that a third-party
// client can reuse: the transport, the protocol handling and the session
// state. Everything the core needs from its environment — a prompt, a key
// derivation, a socket, a clipboard — arrives as an interface, so the core
// never prints, never dials and never reads a terminal itself.
package clientcore

// SecretReason names what a secret is wanted for. The caller owns the
// wording: the built-in client prompts in Vietnamese, a third-party client
// need not, and the core carries no user-facing text.
type SecretReason int

const (
	// ReasonKeyFilePassphrase unlocks an encrypted key.json.
	ReasonKeyFilePassphrase SecretReason = iota
)

// Prompter asks the human for something the core cannot know.
type Prompter interface {
	Secret(reason SecretReason) (string, error)
}
