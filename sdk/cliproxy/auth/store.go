package auth

import "context"

// Store abstracts persistence of Auth state across restarts.
type Store interface {
	// List returns all auth records stored in the backend.
	List(ctx context.Context) ([]*Auth, error)
	// Save persists the provided auth record, replacing any existing one with same ID.
	Save(ctx context.Context, auth *Auth) (string, error)
	// Delete removes the auth record identified by id.
	Delete(ctx context.Context, id string) error
}

// CredentialWriteGate is implemented by components whose credential mutations
// must be fenced by the service lifecycle controller.
type CredentialWriteGate interface {
	SetWritesEnabled(enabled bool)
	WritesEnabled() bool
}
