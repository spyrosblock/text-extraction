package billing

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spyrosblock/text-extraction/billing/blobs"
)

var secret = []byte(strings.Repeat("s", MinFreeCreditsSecret))

func newAccounts() (*Accounts, *blobs.Fake) {
	b := &blobs.Fake{}
	return &Accounts{Blobs: b, Container: "billing", FreeCreditsSecret: secret}, b
}

func TestFindOrCreate(t *testing.T) {
	a, b := newAccounts()
	id, acc, err := a.FindOrCreate(ctx, "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || acc.Email != "ann@example.com" || acc.CreatedAt.IsZero() {
		t.Fatalf("FindOrCreate = %q %+v", id, acc)
	}
	again, _, err := a.FindOrCreate(ctx, "ann@example.com")
	if err != nil || again != id {
		t.Errorf("second FindOrCreate = %q, %v; want %q", again, err, id)
	}
	other, _, err := a.FindOrCreate(ctx, "bob@example.com")
	if err != nil || other == id {
		t.Errorf("other address = %q, %v", other, err)
	}
	if got, err := a.Get(ctx, id); err != nil || got.Email != "ann@example.com" {
		t.Errorf("Get = %+v, %v", got, err)
	}
	for _, n := range b.Names() {
		if strings.HasPrefix(n, "billing/emails/") && strings.Contains(n, "ann") {
			t.Errorf("address in blob name %s", n)
		}
	}
}

// Two first sign-ins for one new address: one account.
func TestFindOrCreateParallel(t *testing.T) {
	a, _ := newAccounts()
	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			id, _, err := a.FindOrCreate(ctx, "ann@example.com")
			if err != nil {
				t.Error(err)
			}
			ids[i] = id
		})
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("ids = %v, want one account", ids)
		}
	}
}

// A failure after the email blob was written is completed by the retry.
func TestFindOrCreateRetry(t *testing.T) {
	a, b := newAccounts()
	b.UploadErr = map[string]error{"billing/accounts/": errors.New("down")}
	if _, _, err := a.FindOrCreate(ctx, "ann@example.com"); err == nil {
		t.Fatal("no error")
	}
	b.UploadErr = nil
	id, acc, err := a.FindOrCreate(ctx, "ann@example.com")
	if err != nil || acc.Email != "ann@example.com" {
		t.Fatalf("retry = %+v, %v", acc, err)
	}
	if _, err := a.Get(ctx, id); err != nil {
		t.Errorf("Get = %v", err)
	}
}

func TestKeyIndexAndDelete(t *testing.T) {
	a, b := newAccounts()
	id, _, err := a.FindOrCreate(ctx, "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	k1, k2 := KeyID("one"), KeyID("two")
	for _, k := range []string{k1, k2} {
		if err := a.AddKey(ctx, id, k); err != nil {
			t.Fatal(err)
		}
	}
	if has, err := a.HasKey(ctx, id, k1); err != nil || !has {
		t.Errorf("HasKey = %v, %v", has, err)
	}
	if has, _ := a.HasKey(ctx, id, KeyID("three")); has {
		t.Error("HasKey of an unindexed key")
	}
	if ids, err := a.KeyIDs(ctx, id); err != nil || len(ids) != 2 {
		t.Errorf("KeyIDs = %v, %v", ids, err)
	}
	if err := a.RemoveKey(ctx, id, k1); err != nil {
		t.Fatal(err)
	}
	if ids, _ := a.KeyIDs(ctx, id); len(ids) != 1 || ids[0] != k2 {
		t.Errorf("KeyIDs after remove = %v", ids)
	}
	if _, err := a.ClaimFreeCredits(ctx, "ann@example.com", k2); err != nil {
		t.Fatal(err)
	}

	if err := a.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Get(ctx, id); !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("Get after delete err = %v", err)
	}
	// Only the free-credit marker is left.
	if names := b.Names(); len(names) != 1 || !strings.HasPrefix(names[0], "billing/freecredits/") {
		t.Errorf("blobs after delete = %v", names)
	}
	newID, _, err := a.FindOrCreate(ctx, "ann@example.com")
	if err != nil || newID == id {
		t.Errorf("sign-up after delete = %q, %v; want a new account", newID, err)
	}
	if err := a.Delete(ctx, id); !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("second Delete err = %v", err)
	}
}

func TestFreeCredits(t *testing.T) {
	a, b := newAccounts()
	const email = "ann@example.com"
	if had, err := a.HadFreeCredits(ctx, email); err != nil || had {
		t.Fatalf("HadFreeCredits = %v, %v", had, err)
	}
	if ok, err := a.ClaimFreeCredits(ctx, email, KeyID("one")); err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	if ok, err := a.ClaimFreeCredits(ctx, email, KeyID("two")); err != nil || ok {
		t.Fatalf("second claim = %v, %v", ok, err)
	}
	if had, _ := a.HadFreeCredits(ctx, email); !had {
		t.Error("HadFreeCredits after claim = false")
	}
	if ok, _ := a.ClaimFreeCredits(ctx, "bob@example.com", KeyID("three")); !ok {
		t.Error("another address can't claim")
	}

	// The marker is named after the HMAC of the address and doesn't hold it.
	names := b.Names()
	for _, n := range names {
		if strings.Contains(n, "ann") || strings.Contains(n, EmailHash(email)) || bytes.Contains(b.Data("billing", strings.TrimPrefix(n, "billing/")), []byte("ann")) {
			t.Errorf("marker %s reveals the address", n)
		}
	}

	// With another secret the same address finds no marker.
	other := &Accounts{Blobs: b, Container: "billing", FreeCreditsSecret: []byte(strings.Repeat("t", MinFreeCreditsSecret))}
	if had, _ := other.HadFreeCredits(ctx, email); had {
		t.Error("marker found with another secret")
	}

	if err := a.ReleaseFreeCredits(ctx, email); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.ClaimFreeCredits(ctx, email, KeyID("four")); !ok {
		t.Error("claim after release failed")
	}
}

func TestFreeCreditsParallel(t *testing.T) {
	a, _ := newAccounts()
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			ok, err := a.ClaimFreeCredits(ctx, "ann@example.com", KeyID(string(rune('a'+i))))
			if err != nil {
				t.Error(err)
			}
			if ok {
				won.Add(1)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Errorf("%d claims won, want 1", won.Load())
	}
}

func TestFreeCreditsNeedsSecret(t *testing.T) {
	a, _ := newAccounts()
	a.FreeCreditsSecret = []byte("short")
	if _, err := a.ClaimFreeCredits(ctx, "ann@example.com", KeyID("one")); err == nil {
		t.Error("claim with a short secret succeeded")
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got := NormalizeEmail("  Ann@Example.COM \n"); got != "ann@example.com" {
		t.Errorf("NormalizeEmail = %q", got)
	}
}
