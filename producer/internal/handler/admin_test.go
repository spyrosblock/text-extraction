package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spyrosblock/text-extraction/producer/internal/blobtest"
	"github.com/spyrosblock/text-extraction/producer/internal/credits"
)

func newAdmin() (*Admin, *blobtest.Fake) {
	b := &blobtest.Fake{}
	return &Admin{Credits: newStore(b)}, b
}

func call(f http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f(w, httptest.NewRequest(method, target, strings.NewReader(body)))
	return w
}

func TestAdminCreateKey(t *testing.T) {
	a, blobs := newAdmin()
	w := call(a.Keys, http.MethodPost, "/api/manage/keys", `{"name":"acme","credits":2000}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	var resp keyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Key, "tx_test_") || resp.ID != credits.KeyID(resp.Key) || resp.Name != "acme" || resp.Credits != 2000 {
		t.Errorf("resp = %+v", resp)
	}
	if blobs.Data("billing", "keys/"+resp.ID+".json") == nil {
		t.Errorf("blob name is not the key's hash: %v", blobs.Names())
	}
	_, k, err := a.Credits.Lookup(context.Background(), resp.Key)
	if err != nil || k.Credits != 2000 {
		t.Errorf("Lookup = %+v, %v", k, err)
	}
}

func TestAdminListSetAddRevoke(t *testing.T) {
	a, _ := newAdmin()
	key, id, _, err := a.Credits.Create(context.Background(), "acme", 10)
	if err != nil {
		t.Fatal(err)
	}
	balanceOf := func() int64 {
		t.Helper()
		_, k, err := a.Credits.Lookup(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		return k.Credits
	}

	w := call(a.Keys, http.MethodGet, "/api/manage/keys", "")
	var list []credits.ListedKey
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != http.StatusOK || len(list) != 1 || list[0].ID != id || list[0].Name != "acme" || list[0].Credits != 10 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), key) {
		t.Error("list shows the key")
	}

	if w := call(a.Balance, http.MethodPut, "/api/manage/credits?key_id="+id, `{"credits":500}`); w.Code != http.StatusOK || balanceOf() != 500 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	if w := call(a.Balance, http.MethodPost, "/api/manage/credits?key_id="+id, `{"delta":-120}`); w.Code != http.StatusOK || balanceOf() != 380 {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}

	if w := call(a.Keys, http.MethodDelete, "/api/manage/keys?key_id="+id, ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if _, _, err := a.Credits.Lookup(context.Background(), key); err != credits.ErrUnknownKey {
		t.Errorf("Lookup after revoke err = %v", err)
	}
	if w := call(a.Keys, http.MethodDelete, "/api/manage/keys?key_id="+id, ""); w.Code != http.StatusNotFound {
		t.Errorf("second revoke: %d", w.Code)
	}
	if w := call(a.Balance, http.MethodPut, "/api/manage/credits?key_id="+id, `{"credits":1}`); w.Code != http.StatusNotFound {
		t.Errorf("set on revoked key: %d", w.Code)
	}
}

func TestAdminBadRequests(t *testing.T) {
	a, _ := newAdmin()
	id := credits.KeyID("whatever")
	tests := []struct {
		name        string
		f           http.HandlerFunc
		method, url string
		body        string
	}{
		{"create without name", a.Keys, http.MethodPost, "/api/manage/keys", `{"credits":1}`},
		{"create without credits", a.Keys, http.MethodPost, "/api/manage/keys", `{"name":"x"}`},
		{"create negative", a.Keys, http.MethodPost, "/api/manage/keys", `{"name":"x","credits":-1}`},
		{"create not json", a.Keys, http.MethodPost, "/api/manage/keys", `name=x`},
		{"revoke without id", a.Keys, http.MethodDelete, "/api/manage/keys", ``},
		{"revoke bad id", a.Keys, http.MethodDelete, "/api/manage/keys?id=../x", ``},
		{"set without credits", a.Balance, http.MethodPut, "/api/manage/credits?key_id=" + id, `{"delta":1}`},
		{"set negative", a.Balance, http.MethodPut, "/api/manage/credits?key_id=" + id, `{"credits":-1}`},
		{"add without delta", a.Balance, http.MethodPost, "/api/manage/credits?key_id=" + id, `{"credits":1}`},
		{"credits bad id", a.Balance, http.MethodPut, "/api/manage/credits?key_id=" + strings.ToUpper(id), `{"credits":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := call(tt.f, tt.method, tt.url, tt.body); w.Code != http.StatusBadRequest {
				t.Errorf("got %d %s, want 400", w.Code, w.Body)
			}
		})
	}
}
