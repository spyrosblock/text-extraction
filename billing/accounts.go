package billing

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spyrosblock/text-extraction/billing/blobs"
)

// ErrUnknownAccount is returned for an account that doesn't exist (any more).
var ErrUnknownAccount = errors.New("unknown account")

// MinFreeCreditsSecret is the shortest FreeCreditsSecret accepted.
const MinFreeCreditsSecret = 32

// Account is stored as accounts/<id>.json.
type Account struct {
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"createdAt"`
}

// emailRecord is stored as emails/<sha256(email)>.json.
type emailRecord struct {
	AccountID string `json:"accountId"`
}

// freeCredits is stored as freecredits/<hmac(email)>.json.
type freeCredits struct {
	KeyID     string    `json:"keyId"`
	GrantedAt time.Time `json:"grantedAt"`
}

// Accounts keeps customer accounts, their key index and the free-credit
// markers. Email addresses are passed normalized (NormalizeEmail).
type Accounts struct {
	Blobs     BlobStore
	Container string
	// FreeCreditsSecret keys the HMAC that names the free-credit markers,
	// at least MinFreeCreditsSecret bytes. A new secret makes every
	// existing marker unmatched.
	FreeCreditsSecret []byte
}

// NormalizeEmail trims and lowercases an email address.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// EmailHash returns the hex sha256 of an email address.
func EmailHash(email string) string { return sha256Hex(email) }

func accountBlob(id string) string           { return "accounts/" + id + ".json" }
func accountKeysPrefix(id string) string     { return "accounts/" + id + "/keys/" }
func accountKeyBlob(id, keyID string) string { return accountKeysPrefix(id) + keyID }
func emailBlob(email string) string          { return "emails/" + EmailHash(email) + ".json" }
func (a *Accounts) freeCreditsBlob(email string) (string, error) {
	if len(a.FreeCreditsSecret) < MinFreeCreditsSecret {
		return "", fmt.Errorf("free credits secret must be at least %d bytes", MinFreeCreditsSecret)
	}
	mac := hmac.New(sha256.New, a.FreeCreditsSecret)
	mac.Write([]byte(email))
	return "freecredits/" + hex.EncodeToString(mac.Sum(nil)) + ".json", nil
}

// FindOrCreate returns the account of email, creating it if there is none.
// Two parallel calls for a new address create one account: the emails/ blob
// is created with If-None-Match, and the loser uses the winner's account.
func (a *Accounts) FindOrCreate(ctx context.Context, email string) (string, Account, error) {
	id := randomHex(16)
	body, err := json.Marshal(emailRecord{AccountID: id})
	if err != nil {
		return "", Account{}, err
	}
	err = a.Blobs.UploadIf(ctx, a.Container, emailBlob(email), body, "application/json", "")
	if errors.Is(err, blobs.ErrConditionNotMet) {
		var rec emailRecord
		if err := a.getJSON(ctx, emailBlob(email), &rec); err != nil {
			return "", Account{}, err
		}
		id = rec.AccountID
	} else if err != nil {
		return "", Account{}, err
	}

	// Created after the email blob, so a retry after a failure here
	// completes the account.
	acc := Account{Email: email, CreatedAt: time.Now().UTC()}
	if body, err = json.Marshal(acc); err != nil {
		return "", Account{}, err
	}
	err = a.Blobs.UploadIf(ctx, a.Container, accountBlob(id), body, "application/json", "")
	if errors.Is(err, blobs.ErrConditionNotMet) {
		acc, err = a.Get(ctx, id)
	}
	if err != nil {
		return "", Account{}, err
	}
	return id, acc, nil
}

// Get returns an account, or ErrUnknownAccount.
func (a *Accounts) Get(ctx context.Context, id string) (Account, error) {
	var acc Account
	err := a.getJSON(ctx, accountBlob(id), &acc)
	if errors.Is(err, blobs.ErrNotFound) {
		return Account{}, ErrUnknownAccount
	}
	return acc, err
}

// Delete removes the account, its key index and its email blob, but not the
// keys themselves (revoke them first) nor the free-credit marker. The email
// blob goes first: a sign-in from then on creates a new account rather than
// bringing this one back.
func (a *Accounts) Delete(ctx context.Context, id string) error {
	acc, err := a.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := a.deleteIfExists(ctx, emailBlob(acc.Email)); err != nil {
		return err
	}
	keyIDs, err := a.KeyIDs(ctx, id)
	if err != nil {
		return err
	}
	for _, keyID := range keyIDs {
		if err := a.RemoveKey(ctx, id, keyID); err != nil {
			return err
		}
	}
	return a.deleteIfExists(ctx, accountBlob(id))
}

// AddKey adds a key to the account's index.
func (a *Accounts) AddKey(ctx context.Context, id, keyID string) error {
	return a.Blobs.Upload(ctx, a.Container, accountKeyBlob(id, keyID), nil, "application/octet-stream")
}

// RemoveKey removes a key from the account's index.
func (a *Accounts) RemoveKey(ctx context.Context, id, keyID string) error {
	return a.deleteIfExists(ctx, accountKeyBlob(id, keyID))
}

// HasKey reports whether the key is in the account's index. Ownership also
// needs the key's AccountID to match.
func (a *Accounts) HasKey(ctx context.Context, id, keyID string) (bool, error) {
	_, _, err := a.Blobs.Get(ctx, a.Container, accountKeyBlob(id, keyID))
	if errors.Is(err, blobs.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// KeyIDs returns the ids in the account's key index.
func (a *Accounts) KeyIDs(ctx context.Context, id string) ([]string, error) {
	names, err := a.Blobs.List(ctx, a.Container, accountKeysPrefix(id))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		if keyID := strings.TrimPrefix(name, accountKeysPrefix(id)); ValidID(keyID) {
			ids = append(ids, keyID)
		}
	}
	return ids, nil
}

// ClaimFreeCredits records that email has had its free credits, with the
// key that got them. It returns false if the address had them already. Of
// two parallel claims only one succeeds.
func (a *Accounts) ClaimFreeCredits(ctx context.Context, email, keyID string) (bool, error) {
	name, err := a.freeCreditsBlob(email)
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(freeCredits{KeyID: keyID, GrantedAt: time.Now().UTC()})
	if err != nil {
		return false, err
	}
	err = a.Blobs.UploadIf(ctx, a.Container, name, body, "application/json", "")
	if errors.Is(err, blobs.ErrConditionNotMet) {
		return false, nil
	}
	return err == nil, err
}

// ReleaseFreeCredits undoes ClaimFreeCredits, when the key that was to get
// the credits couldn't be created.
func (a *Accounts) ReleaseFreeCredits(ctx context.Context, email string) error {
	name, err := a.freeCreditsBlob(email)
	if err != nil {
		return err
	}
	return a.deleteIfExists(ctx, name)
}

// HadFreeCredits reports whether email has had its free credits.
func (a *Accounts) HadFreeCredits(ctx context.Context, email string) (bool, error) {
	name, err := a.freeCreditsBlob(email)
	if err != nil {
		return false, err
	}
	_, _, err = a.Blobs.Get(ctx, a.Container, name)
	if errors.Is(err, blobs.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (a *Accounts) getJSON(ctx context.Context, name string, v any) error {
	return getJSON(ctx, a.Blobs, a.Container, name, v)
}

func (a *Accounts) deleteIfExists(ctx context.Context, name string) error {
	if err := a.Blobs.Delete(ctx, a.Container, name); err != nil && !errors.Is(err, blobs.ErrNotFound) {
		return err
	}
	return nil
}

func getJSON(ctx context.Context, b BlobStore, container, name string, v any) error {
	data, _, err := b.Get(ctx, container, name)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// randomHex returns n random bytes in hex.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b) // never fails
	return hex.EncodeToString(b)
}

// newToken returns 32 random bytes in base64url, for sessions and login
// tokens. Only its sha256 is stored.
func newToken() string {
	b := make([]byte, 32)
	rand.Read(b) // never fails
	return base64.RawURLEncoding.EncodeToString(b)
}
