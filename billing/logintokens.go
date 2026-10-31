package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spyrosblock/text-extraction/billing/blobs"
)

// ErrInvalidToken is returned for an unknown, used or expired login token.
var ErrInvalidToken = errors.New("invalid or expired login token")

// loginToken is stored as logintokens/<sha256(token)>.json.
type loginToken struct {
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// LoginTokens keeps the magic-link tokens. Each can be consumed once. A
// lifecycle rule deletes leftover blobs after a day.
type LoginTokens struct {
	Blobs     BlobStore
	Container string
	TTL       time.Duration
	// Now is the clock; time.Now if nil.
	Now func() time.Time
}

func loginTokenBlob(token string) string { return "logintokens/" + sha256Hex(token) + ".json" }

// Create returns a new token for email.
func (l *LoginTokens) Create(ctx context.Context, email string) (string, error) {
	token := newToken()
	body, err := json.Marshal(loginToken{Email: email, ExpiresAt: now(l.Now).Add(l.TTL)})
	if err != nil {
		return "", err
	}
	if err := l.Blobs.UploadIf(ctx, l.Container, loginTokenBlob(token), body, "application/json", ""); err != nil {
		return "", err
	}
	return token, nil
}

// Consume returns the email of a token and deletes the token. The delete is
// conditional on the ETag that was read, so of two parallel calls only one
// gets the email; the other gets ErrInvalidToken.
func (l *LoginTokens) Consume(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrInvalidToken
	}
	name := loginTokenBlob(token)
	data, etag, err := l.Blobs.Get(ctx, l.Container, name)
	if errors.Is(err, blobs.ErrNotFound) {
		return "", ErrInvalidToken
	}
	if err != nil {
		return "", err
	}
	var t loginToken
	if err := json.Unmarshal(data, &t); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if !now(l.Now).Before(t.ExpiresAt) {
		return "", ErrInvalidToken
	}
	err = l.Blobs.DeleteIf(ctx, l.Container, name, etag)
	if errors.Is(err, blobs.ErrNotFound) || errors.Is(err, blobs.ErrConditionNotMet) {
		return "", ErrInvalidToken
	}
	if err != nil {
		return "", err
	}
	return t.Email, nil
}
