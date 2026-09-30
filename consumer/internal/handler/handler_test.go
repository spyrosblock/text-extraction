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

	"github.com/knowledge/text-extraction/consumer/internal/azure"
	"github.com/knowledge/text-extraction/consumer/internal/contract"
)

const id = "0b6f2c4e-8d1a-4f3b-9c2d-7e5a1b3c4d5f"

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

func newHandler(blobs map[string]blob) (*Handler, *fakeBlobs) {
	b := &fakeBlobs{blobs: blobs}
	return &Handler{Blobs: b, TextContainer: "text-storage", Now: func() time.Time { return now }}, b
}

func post(h *Handler, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/text", strings.NewReader(body)))
	return w
}

func idBody(id string) string { return `{"id":"` + id + `"}` }

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
			if len(blobs.opened) != 1 || blobs.opened[0] != "text-storage/"+id+".json" {
				t.Errorf("opened = %v", blobs.opened)
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
		"too large":  `{"id":` + strings.Repeat(" ", maxRequestBytes) + `"` + id + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h, blobs := newHandler(nil)
			if w := post(h, body); w.Code != http.StatusBadRequest {
				t.Fatalf("got %d %s, want 400", w.Code, w.Body)
			}
			if len(blobs.opened) != 0 {
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
