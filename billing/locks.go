package billing

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/spyrosblock/text-extraction/billing/blobs"
)

// ErrLocked is returned by Locks.Acquire while someone else holds the lock.
var ErrLocked = errors.New("locked")

// Locks are named mutual-exclusion locks across instances, stored as
// locks/<name>.json: a lock is held while its blob exists and hasn't
// expired. An expired lock (its holder crashed) is taken over.
type Locks struct {
	Blobs     BlobStore
	Container string
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

type lockRecord struct {
	ExpiresAt time.Time `json:"expiresAt"`
}

func lockBlob(name string) string { return "locks/" + name + ".json" }

// Acquire takes the lock name for up to ttl. It returns ErrLocked if the lock
// is held, and otherwise a function that releases it; releasing a lock that
// expired and was taken over leaves the new holder's lock alone.
func (l *Locks) Acquire(ctx context.Context, name string, ttl time.Duration) (release func(context.Context) error, err error) {
	blob := lockBlob(name)
	body, err := json.Marshal(lockRecord{ExpiresAt: now(l.Now).Add(ttl).UTC()})
	if err != nil {
		return nil, err
	}
	// Twice: once more after removing an expired lock.
	for range 2 {
		err := l.Blobs.UploadIf(ctx, l.Container, blob, body, "application/json", "")
		if err == nil {
			// The new blob's ETag isn't returned by UploadIf; read it, so
			// release only deletes this lock.
			_, etag, err := l.Blobs.Get(ctx, l.Container, blob)
			if errors.Is(err, blobs.ErrNotFound) {
				return nil, ErrLocked // expired and taken over already
			}
			if err != nil {
				return nil, err
			}
			return func(ctx context.Context) error {
				err := l.Blobs.DeleteIf(ctx, l.Container, blob, etag)
				if errors.Is(err, blobs.ErrConditionNotMet) || errors.Is(err, blobs.ErrNotFound) {
					return nil
				}
				return err
			}, nil
		}
		if !errors.Is(err, blobs.ErrConditionNotMet) {
			return nil, err
		}
		data, etag, err := l.Blobs.Get(ctx, l.Container, blob)
		if errors.Is(err, blobs.ErrNotFound) {
			continue // released meanwhile
		}
		if err != nil {
			return nil, err
		}
		// An unreadable lock counts as expired, so it can't block forever.
		var held lockRecord
		if json.Unmarshal(data, &held) == nil && now(l.Now).Before(held.ExpiresAt) {
			return nil, ErrLocked
		}
		err = l.Blobs.DeleteIf(ctx, l.Container, blob, etag)
		if err != nil && !errors.Is(err, blobs.ErrConditionNotMet) && !errors.Is(err, blobs.ErrNotFound) {
			return nil, err
		}
	}
	return nil, ErrLocked
}
