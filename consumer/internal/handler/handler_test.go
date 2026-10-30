package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spyrosblock/text-extraction/consumer/internal/azure"
	"github.com/spyrosblock/text-extraction/consumer/internal/contract"
)

const id = "0b6f2c4e-8d1a-4f3b-9c2d-7e5a1b3c4d5f"

const apiKey = "tx_test_key"

// apiKeyID is sha256("tx_test_key"), as the producer computes it.
const apiKeyID = "197f57455b15654af2963c7b62d520663eb95f3a1cd94676ffa8082b5dba2d4a"

var now = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

type blob struct {
	data     string
	modified time.Time
}

type fakeBlobs struct {
	blobs  map[string]blob // "container/name" -> blob
	err    error
	opened []string
}

func (f *fakeBlobs) Open(_ context.Context, container, name string) (io.ReadCloser, time.Time, error) {
	f.opened = append(f.opened, container+"/"+name)
	if f.err != nil {
		return nil, time.Time{}, f.err
	}
	b, ok := f.blobs[container+"/"+name]
	if !ok {
		return nil, time.Time{}, azure.ErrNotFound
	}
	return io.NopCloser(strings.NewReader(b.data)), b.modified, nil
}

// newHandler serves blobs plus the billing blobs of apiKey and of a job
// with the test id submitted with it.
func newHandler(blobs map[string]blob) (*Handler, *fakeBlobs) {
	all := map[string]blob{
		"billing/keys/" + apiKeyID + ".json": {`{"name":"test","credits":10}`, now},
		"billing/jobs/" + id + ".json":       {`{"keyId":"` + apiKeyID + `","pages":2}`, now},
	}
	for k, v := range blobs {
		all[k] = v
	}
	b := &fakeBlobs{blobs: all}
	return &Handler{Blobs: b, TextContainer: "text-storage", BillingContainer: "billing", Now: func() time.Time { return now }}, b
}

func postAs(h *Handler, key, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/text", strings.NewReader(body))
	if key != "" {
		r.Header.Set(APIKeyHeader, key)
	}
	h.ServeHTTP(w, r)
	return w
}

func post(h *Handler, body string) *httptest.ResponseRecorder { return postAs(h, apiKey, body) }

// textOpened reports whether the handler read any text blob.
func (f *fakeBlobs) textOpened() bool {
	for _, o := range f.opened {
		if strings.HasPrefix(o, "text-storage/") {
			return true
		}
	}
	return false
}

func idBody(id string) string { return `{"request_id":"` + id + `"}` }

func storedDoc(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(contract.Document{ID: id, Pages: []contract.Page{{Number: 1, Text: "one"}, {Number: 2, Text: "δύο"}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReturnsDocument(t *testing.T) {
	doc := storedDoc(t)
	for name, body := range map[string]string{
		"lowercase": idBody(id),
		"uppercase": idBody(strings.ToUpper(id)),
	} {
		t.Run(name, func(t *testing.T) {
			h, blobs := newHandler(map[string]blob{"text-storage/" + id + ".json": {doc, now.Add(-time.Hour)}})
			w := post(h, body)

			if w.Code != http.StatusOK {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			var got contract.Document
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.ID != id || len(got.Pages) != 2 || got.Pages[1].Text != "δύο" {
				t.Errorf("doc = %+v", got)
			}
			if want := []string{"billing/keys/" + apiKeyID + ".json", "billing/jobs/" + id + ".json", "text-storage/" + id + ".json"}; strings.Join(blobs.opened, " ") != strings.Join(want, " ") {
				t.Errorf("opened = %v, want %v", blobs.opened, want)
			}
		})
	}
}

func TestNotFound(t *testing.T) {
	doc := storedDoc(t)
	for name, blobs := range map[string]map[string]blob{
		"missing": nil,
		"expired": {"text-storage/" + id + ".json": {doc, now.Add(-Retention - time.Minute)}},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newHandler(blobs)
			if w := post(h, idBody(id)); w.Code != http.StatusNotFound {
				t.Fatalf("got %d %s, want 404", w.Code, w.Body)
			}
		})
	}
}

func TestInvalidID(t *testing.T) {
	for name, body := range map[string]string{
		"not a uuid": idBody("not-a-uuid"),
		"braces":     idBody("{" + id + "}"),
		"urn":        idBody("urn:uuid:" + id),
		"empty id":   idBody(""),
		"no id":      `{}`,
		"empty body": ``,
		"not json":   id,
		"too large":  `{"request_id":` + strings.Repeat(" ", maxRequestBytes) + `"` + id + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h, blobs := newHandler(nil)
			if w := post(h, body); w.Code != http.StatusBadRequest {
				t.Fatalf("got %d %s, want 400", w.Code, w.Body)
			}
			if blobs.textOpened() {
				t.Errorf("opened = %v", blobs.opened)
			}
		})
	}
}

func TestStorageError(t *testing.T) {
	h, blobs := newHandler(nil)
	blobs.err = errors.New("storage down")
	if w := post(h, idBody(id)); w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d %s, want 500", w.Code, w.Body)
	}
}

func TestKeyID(t *testing.T) {
	if got := keyID(apiKey); got != apiKeyID {
		t.Fatalf("keyID = %s, want %s", got, apiKeyID)
	}
}

func TestUnauthorized(t *testing.T) {
	doc := storedDoc(t)
	for name, key := range map[string]string{
		"no key":      "",
		"unknown key": "tx_test_other",
	} {
		t.Run(name, func(t *testing.T) {
			h, blobs := newHandler(map[string]blob{"text-storage/" + id + ".json": {doc, now}})
			if w := postAs(h, key, idBody(id)); w.Code != http.StatusUnauthorized {
				t.Fatalf("got %d %s, want 401", w.Code, w.Body)
			}
			if blobs.textOpened() {
				t.Errorf("opened = %v", blobs.opened)
			}
		})
	}
}

func TestOtherKeysJob(t *testing.T) {
	doc := storedDoc(t)
	const otherKeyID = "1111111111111111111111111111111111111111111111111111111111111111"
	for name, job := range map[string]*blob{
		"job of another key": {`{"keyId":"` + otherKeyID + `"}`, now},
		"no job record":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			h, blobs := newHandler(map[string]blob{"text-storage/" + id + ".json": {doc, now}})
			if job != nil {
				blobs.blobs["billing/jobs/"+id+".json"] = *job
			} else {
				delete(blobs.blobs, "billing/jobs/"+id+".json")
			}
			if w := post(h, idBody(id)); w.Code != http.StatusNotFound {
				t.Fatalf("got %d %s, want 404", w.Code, w.Body)
			}
			if blobs.textOpened() {
				t.Errorf("opened = %v", blobs.opened)
			}
		})
	}
}
