package credits

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/knowledge/text-extraction/producer/internal/blobtest"
)

var ctx = context.Background()

func newStore() (*Store, *blobtest.Fake) {
	b := &blobtest.Fake{}
	return &Store{
		Blobs: b, Container: "billing", KeyPrefix: "tx_test_",
		TextPageCredits: 1, OCRPageCredits: 5,
		backoff: func(int) time.Duration { return 0 },
	}, b
}

func create(t *testing.T, s *Store, credits int64) (key, id string) {
	t.Helper()
	key, id, _, err := s.Create(ctx, "acme", credits)
	if err != nil {
		t.Fatal(err)
	}
	return key, id
}

func balance(t *testing.T, s *Store, key string) int64 {
	t.Helper()
	_, k, err := s.Lookup(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	return k.Credits
}

func TestCreate(t *testing.T) {
	s, blobs := newStore()
	key, id, k, err := s.Create(ctx, "acme", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "tx_test_") || len(key) != len("tx_test_")+keyChars {
		t.Errorf("key = %q", key)
	}
	if id != KeyID(key) || !ValidID(id) {
		t.Errorf("id = %q, want sha256 of the key", id)
	}
	if k.Name != "acme" || k.Credits != 100 || k.CreatedAt.IsZero() {
		t.Errorf("key = %+v", k)
	}
	if names := blobs.Names(); len(names) != 1 || names[0] != "billing/keys/"+id+".json" {
		t.Errorf("blobs = %v", names)
	}
	if strings.Contains(string(blobs.Data("billing", "keys/"+id+".json")), key) {
		t.Error("key stored in the blob")
	}
	if other, _ := create(t, s, 0); other == key {
		t.Error("two keys are equal")
	}
}

func TestLookup(t *testing.T) {
	s, _ := newStore()
	key, id := create(t, s, 7)
	gotID, k, err := s.Lookup(ctx, key)
	if err != nil || gotID != id || k.Credits != 7 {
		t.Errorf("Lookup = %s %+v %v", gotID, k, err)
	}
	for _, bad := range []string{"", "tx_test_nope", key + "x"} {
		if _, _, err := s.Lookup(ctx, bad); !errors.Is(err, ErrUnknownKey) {
			t.Errorf("Lookup(%q) err = %v, want ErrUnknownKey", bad, err)
		}
	}
}

func TestCost(t *testing.T) {
	s, _ := newStore()
	if got := s.Cost(10, 0); got != 10 {
		t.Errorf("Cost(10, 0) = %d", got)
	}
	if got := s.Cost(10, 3); got != 7+15 {
		t.Errorf("Cost(10, 3) = %d", got)
	}
}

func TestCharge(t *testing.T) {
	s, _ := newStore()
	key, id := create(t, s, 10)

	if bal, err := s.Charge(ctx, id, 4); err != nil || bal != 6 {
		t.Fatalf("Charge = %d, %v; want 6", bal, err)
	}
	_, err := s.Charge(ctx, id, 7)
	var short *InsufficientError
	if !errors.As(err, &short) || short.Required != 7 || short.Balance != 6 {
		t.Fatalf("Charge err = %v, want insufficient 7/6", err)
	}
	if got := balance(t, s, key); got != 6 {
		t.Errorf("balance after refused charge = %d, want 6", got)
	}
	if bal, err := s.Charge(ctx, id, 6); err != nil || bal != 0 {
		t.Fatalf("Charge = %d, %v; want 0", bal, err)
	}
	if _, err := s.Charge(ctx, KeyID("other"), 1); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("Charge unknown key err = %v", err)
	}
}

// A concurrent write between read and write makes the conditional upload
// fail; the charge is redone on the new balance.
func TestChargeRetriesOnConflict(t *testing.T) {
	s, blobs := newStore()
	key, id := create(t, s, 10)
	conflicts := 2
	blobs.BeforeUploadIf = func(container, name string) {
		if conflicts > 0 {
			conflicts--
			// Someone else spends 3 credits.
			blobs.Put(container, name, mustJSON(t, Key{Name: "acme", Credits: blobBalance(t, blobs, id) - 3}))
		}
	}
	bal, err := s.Charge(ctx, id, 2)
	if err != nil || bal != 2 {
		t.Fatalf("Charge = %d, %v; want 10-3-3-2 = 2", bal, err)
	}
	if got := balance(t, s, key); got != 2 {
		t.Errorf("stored balance = %d", got)
	}

	// The re-check uses the fresh balance.
	conflicts = 1
	_, err = s.Charge(ctx, id, 2)
	var short *InsufficientError
	if !errors.As(err, &short) || short.Balance != -1 {
		t.Fatalf("Charge err = %v, want insufficient on the fresh balance", err)
	}
}

func TestChargeGivesUp(t *testing.T) {
	s, blobs := newStore()
	key, id := create(t, s, 10)
	tries := 0
	blobs.BeforeUploadIf = func(container, name string) {
		tries++
		blobs.Put(container, name, blobs.Data(container, name)) // new ETag, same content
	}
	if _, err := s.Charge(ctx, id, 1); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if tries != 1+maxRetries {
		t.Errorf("tries = %d", tries)
	}
	if got := balance(t, s, key); got != 10 {
		t.Errorf("balance = %d, want 10", got)
	}
}

func TestSetAddDelete(t *testing.T) {
	s, _ := newStore()
	key, id := create(t, s, 10)

	if k, err := s.Set(ctx, id, 50); err != nil || k.Credits != 50 {
		t.Fatalf("Set = %+v, %v", k, err)
	}
	if k, err := s.Add(ctx, id, -20); err != nil || k.Credits != 30 || k.Name != "acme" {
		t.Fatalf("Add = %+v, %v", k, err)
	}
	if got := balance(t, s, key); got != 30 {
		t.Errorf("balance = %d", got)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Lookup(ctx, key); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("Lookup after delete err = %v", err)
	}
	if err := s.Delete(ctx, id); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("second Delete err = %v", err)
	}
	if _, err := s.Set(ctx, id, 1); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("Set after delete err = %v", err)
	}
}

func TestList(t *testing.T) {
	s, _ := newStore()
	_, id1 := create(t, s, 1)
	_, id2 := create(t, s, 2)
	if err := s.RecordJob(ctx, "job", Job{KeyID: id1}); err != nil {
		t.Fatal(err)
	}
	keys, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, k := range keys {
		got[k.ID] = k.Credits
	}
	if len(keys) != 2 || got[id1] != 1 || got[id2] != 2 {
		t.Errorf("List = %+v", keys)
	}
}

func TestValidID(t *testing.T) {
	id := KeyID("k")
	for in, want := range map[string]bool{
		id:                      true,
		strings.ToUpper(id):     false,
		id[1:]:                  false,
		"../" + id[3:]:          false,
		strings.Repeat("g", 64): false,
	} {
		if ValidID(in) != want {
			t.Errorf("ValidID(%q) = %v", in, !want)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func blobBalance(t *testing.T, blobs *blobtest.Fake, id string) int64 {
	t.Helper()
	var k Key
	if err := json.Unmarshal(blobs.Data("billing", "keys/"+id+".json"), &k); err != nil {
		t.Fatal(err)
	}
	return k.Credits
}
