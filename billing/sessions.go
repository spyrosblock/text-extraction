package billing

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/spyrosblock/text-extraction/billing/blobs"
)

// ErrNoSession is returned for a missing, unknown or expired session, or one
// whose account was deleted.
var ErrNoSession = errors.New("no session")

// Session is stored as sessions/<sha256(token)>.json.
type Session struct {
	AccountID string    `json:"accountId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Sessions keeps the sign-in sessions. A lifecycle rule deletes the blobs a
// day after they expire; Resolve checks ExpiresAt itself.
type Sessions struct {
	Blobs     BlobStore
	Container string
	Accounts  *Accounts
	TTL       time.Duration
	// Now is the clock; time.Now if nil.
	Now func() time.Time
}

func sessionBlob(token string) string { return "sessions/" + sha256Hex(token) + ".json" }

// Create starts a session for the account and returns its token.
func (s *Sessions) Create(ctx context.Context, accountID string) (string, error) {
	token := newToken()
	body, err := json.Marshal(Session{AccountID: accountID, ExpiresAt: now(s.Now).Add(s.TTL)})
	if err != nil {
		return "", err
	}
	if err := s.Blobs.UploadIf(ctx, s.Container, sessionBlob(token), body, "application/json", ""); err != nil {
		return "", err
	}
	return token, nil
}

// Resolve returns the account of a session token, or ErrNoSession.
func (s *Sessions) Resolve(ctx context.Context, token string) (string, Account, error) {
	if token == "" {
		return "", Account{}, ErrNoSession
	}
	var sess Session
	err := getJSON(ctx, s.Blobs, s.Container, sessionBlob(token), &sess)
	if errors.Is(err, blobs.ErrNotFound) {
		return "", Account{}, ErrNoSession
	}
	if err != nil {
		return "", Account{}, err
	}
	if !now(s.Now).Before(sess.ExpiresAt) {
		return "", Account{}, ErrNoSession
	}
	acc, err := s.Accounts.Get(ctx, sess.AccountID)
	if errors.Is(err, ErrUnknownAccount) {
		return "", Account{}, ErrNoSession
	}
	if err != nil {
		return "", Account{}, err
	}
	return sess.AccountID, acc, nil
}

// Delete ends a session. Deleting an unknown session is not an error.
func (s *Sessions) Delete(ctx context.Context, token string) error {
	err := s.Blobs.Delete(ctx, s.Container, sessionBlob(token))
	if errors.Is(err, blobs.ErrNotFound) {
		return nil
	}
	return err
}

func now(clock func() time.Time) time.Time {
	if clock != nil {
		return clock().UTC()
	}
	return time.Now().UTC()
}
