package billing

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	mrand "math/rand/v2"
	"strings"
	"time"

	"github.com/spyrosblock/text-extraction/billing/blobs"
)

var (
	// ErrUnknownKey is returned for a missing, unknown or revoked key.
	ErrUnknownKey = errors.New("unknown api key")
	// ErrBusy is returned when a balance kept changing under every retry.
	ErrBusy = errors.New("too many concurrent balance updates")
)

// InsufficientError is returned by Charge when the balance doesn't cover
// the cost. Nothing is charged.
type InsufficientError struct {
	Required, Balance int64
}

func (e *InsufficientError) Error() string {
	return fmt.Sprintf("%d credits required, balance is %d", e.Required, e.Balance)
}

type BlobStore interface {
	Get(ctx context.Context, container, name string) ([]byte, string, error)
	Upload(ctx context.Context, container, name string, data []byte, contentType string) error
	UploadIf(ctx context.Context, container, name string, data []byte, contentType, ifMatch string) error
	Delete(ctx context.Context, container, name string) error
	DeleteIf(ctx context.Context, container, name, ifMatch string) error
	List(ctx context.Context, container, prefix string) ([]string, error)
}

// Key is stored as keys/<id>.json. Every balance change writes the whole
// blob back, so fields added here are kept by charges.
type Key struct {
	Name      string    `json:"name"`
	Credits   int64     `json:"credits"`
	CreatedAt time.Time `json:"createdAt"`
	// AccountID owns the key; empty for keys issued with infra/keys.sh.
	AccountID string `json:"accountId,omitempty"`
	// Hint is the key's last characters, to tell keys apart.
	Hint string `json:"hint,omitempty"`
	// Orders are the purchases credited to the key, by order id.
	Orders map[string]Order `json:"orders,omitempty"`
}

// Order is a purchase credited to a key.
type Order struct {
	Pack      string    `json:"pack"`
	Credits   int64     `json:"credits"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListedKey is a Key with its id, as returned by List.
type ListedKey struct {
	ID string `json:"key_id"`
	Key
}

// Job is stored as jobs/<job id>.json when a job is charged. The consumer
// only serves a job's text to the key in KeyID.
type Job struct {
	KeyID     string    `json:"keyId"`
	Pages     int       `json:"pages"`
	OCRPages  int       `json:"ocrPages"`
	Credits   int64     `json:"credits"`
	CreatedAt time.Time `json:"createdAt"`
}

// maxRetries is how often a balance update is retried after a conflict.
const maxRetries = 5

// keyBytes is the number of random bytes in a key.
const keyBytes = 32

// keyChars is the length of keyBytes in base62: 62^43 > 2^256.
const keyChars = 43

// hintChars is how many of the key's last characters Hint keeps.
const hintChars = 4

type Store struct {
	Blobs     BlobStore
	Container string
	// KeyPrefix starts every new key, e.g. "tx_prod_".
	KeyPrefix string
	// Price of a page with a text layer and of a page that needs OCR.
	TextPageCredits int64
	OCRPageCredits  int64

	// backoff is the wait before retry n (1-based); overridden in tests.
	backoff func(n int) time.Duration
}

// KeyID returns the id of an API key: the hex sha256 of the key. The
// consumer has the same one-liner; keep them in sync.
func KeyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// ValidID reports whether id looks like a KeyID.
func ValidID(id string) bool {
	if len(id) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

func keyBlob(id string) string { return "keys/" + id + ".json" }

// JobBlob returns the billing blob name of a job.
func JobBlob(jobID string) string { return "jobs/" + jobID + ".json" }

// Cost returns the credits for a PDF of pages pages, ocrPages of which
// need OCR.
func (s *Store) Cost(pages, ocrPages int) int64 {
	return int64(pages-ocrPages)*s.TextPageCredits + int64(ocrPages)*s.OCRPageCredits
}

// Lookup returns the id and stored state of an API key.
func (s *Store) Lookup(ctx context.Context, key string) (string, Key, error) {
	if key == "" {
		return "", Key{}, ErrUnknownKey
	}
	id := KeyID(key)
	k, _, err := s.get(ctx, id)
	return id, k, err
}

// Charge takes cost credits from the key's balance and returns the new
// balance. It returns *InsufficientError and charges nothing if the balance
// is lower than cost.
func (s *Store) Charge(ctx context.Context, id string, cost int64) (int64, error) {
	k, err := s.update(ctx, id, func(k *Key) error {
		if k.Credits < cost {
			return &InsufficientError{Required: cost, Balance: k.Credits}
		}
		k.Credits -= cost
		return nil
	})
	return k.Credits, err
}

// Set sets the key's balance.
func (s *Store) Set(ctx context.Context, id string, credits int64) (Key, error) {
	return s.update(ctx, id, func(k *Key) error {
		k.Credits = credits
		return nil
	})
}

// Add adds delta (which may be negative) to the key's balance.
func (s *Store) Add(ctx context.Context, id string, delta int64) (Key, error) {
	return s.update(ctx, id, func(k *Key) error {
		k.Credits += delta
		return nil
	})
}

// CreditOrder adds credits to the key's balance for the purchase orderID and
// records it in the key's orders. If the order is already there, it changes
// nothing and returns credited = false, so a retried purchase is safe.
// Returns the balance afterwards.
func (s *Store) CreditOrder(ctx context.Context, id, orderID, pack string, credits int64) (balance int64, credited bool, err error) {
	k, err := s.update(ctx, id, func(k *Key) error {
		if _, ok := k.Orders[orderID]; ok {
			return errUnchanged
		}
		if k.Orders == nil {
			k.Orders = map[string]Order{}
		}
		k.Orders[orderID] = Order{Pack: pack, Credits: credits, CreatedAt: time.Now().UTC()}
		k.Credits += credits
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return k.Credits, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return k.Credits, true, nil
}

// errUnchanged stops an update without writing.
var errUnchanged = errors.New("unchanged")

// Create issues a new key for accountID (empty for an admin-issued key).
// The key is only returned here; the store keeps its id.
func (s *Store) Create(ctx context.Context, name string, credits int64, accountID string) (key, id string, k Key, err error) {
	key, err = s.NewKey()
	if err != nil {
		return "", "", Key{}, err
	}
	id, k, err = s.Insert(ctx, key, name, credits, accountID)
	if err != nil {
		return "", "", Key{}, err
	}
	return key, id, k, nil
}

// NewKey returns a new random key, KeyPrefix followed by keyBytes random
// bytes in base62. Nothing is stored until Insert.
func (s *Store) NewKey() (string, error) {
	b := make([]byte, keyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	n := new(big.Int).SetBytes(b).Text(62)
	return s.KeyPrefix + strings.Repeat("0", keyChars-len(n)) + n, nil
}

// Insert stores a key from NewKey. It fails with blobs.ErrConditionNotMet
// if the key exists already.
func (s *Store) Insert(ctx context.Context, key, name string, credits int64, accountID string) (string, Key, error) {
	id := KeyID(key)
	k := Key{
		Name: name, Credits: credits, CreatedAt: time.Now().UTC(),
		AccountID: accountID, Hint: key[max(0, len(key)-hintChars):],
	}
	body, err := json.Marshal(k)
	if err != nil {
		return "", Key{}, err
	}
	if err := s.Blobs.UploadIf(ctx, s.Container, keyBlob(id), body, "application/json", ""); err != nil {
		return "", Key{}, err
	}
	return id, k, nil
}

// Get returns the stored state of the key with this id.
func (s *Store) Get(ctx context.Context, id string) (Key, error) {
	k, _, err := s.get(ctx, id)
	return k, err
}

// Delete revokes a key.
func (s *Store) Delete(ctx context.Context, id string) error {
	err := s.Blobs.Delete(ctx, s.Container, keyBlob(id))
	if errors.Is(err, blobs.ErrNotFound) {
		return ErrUnknownKey
	}
	return err
}

// List returns every key, sorted by id.
func (s *Store) List(ctx context.Context) ([]ListedKey, error) {
	names, err := s.Blobs.List(ctx, s.Container, "keys/")
	if err != nil {
		return nil, err
	}
	keys := make([]ListedKey, 0, len(names))
	for _, name := range names {
		id, ok := strings.CutSuffix(strings.TrimPrefix(name, "keys/"), ".json")
		if !ok || !ValidID(id) {
			continue
		}
		k, _, err := s.get(ctx, id)
		if errors.Is(err, ErrUnknownKey) { // revoked meanwhile
			continue
		}
		if err != nil {
			return nil, err
		}
		keys = append(keys, ListedKey{ID: id, Key: k})
	}
	return keys, nil
}

// RecordJob stores who submitted a job.
func (s *Store) RecordJob(ctx context.Context, jobID string, job Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return s.Blobs.Upload(ctx, s.Container, JobBlob(jobID), body, "application/json")
}

func (s *Store) get(ctx context.Context, id string) (Key, string, error) {
	data, etag, err := s.Blobs.Get(ctx, s.Container, keyBlob(id))
	if errors.Is(err, blobs.ErrNotFound) {
		return Key{}, "", ErrUnknownKey
	}
	if err != nil {
		return Key{}, "", err
	}
	var k Key
	if err := json.Unmarshal(data, &k); err != nil {
		return Key{}, "", fmt.Errorf("key %s: %w", id, err)
	}
	return k, etag, nil
}

// update applies change to the key and writes it back if the blob hasn't
// changed since it was read, retrying up to maxRetries times otherwise.
// An error from change is returned as is and nothing is written.
func (s *Store) update(ctx context.Context, id string, change func(*Key) error) (Key, error) {
	for attempt := 0; ; attempt++ {
		k, etag, err := s.get(ctx, id)
		if err != nil {
			return Key{}, err
		}
		if err := change(&k); err != nil {
			return k, err
		}
		body, err := json.Marshal(k)
		if err != nil {
			return Key{}, err
		}
		err = s.Blobs.UploadIf(ctx, s.Container, keyBlob(id), body, "application/json", etag)
		if err == nil {
			return k, nil
		}
		if !errors.Is(err, blobs.ErrConditionNotMet) {
			return Key{}, err
		}
		if attempt == maxRetries {
			return Key{}, ErrBusy
		}
		select {
		case <-time.After(wait(s.backoff, attempt+1)):
		case <-ctx.Done():
			return Key{}, ctx.Err()
		}
	}
}

// wait returns backoff(n), or a random backoff of up to 25ms times n if
// backoff is nil.
func wait(backoff func(int) time.Duration, n int) time.Duration {
	if backoff != nil {
		return backoff(n)
	}
	return mrand.N(time.Duration(n) * 25 * time.Millisecond)
}
