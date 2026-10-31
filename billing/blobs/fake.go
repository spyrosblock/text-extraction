package blobs

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Fake is an in-memory stand-in for Blobs in unit tests. It keeps blobs
// under "container/name", each with an ETag that changes on every write, and
// is safe for concurrent use.
type Fake struct {
	mu      sync.Mutex
	blobs   map[string]fakeBlob
	etag    int
	Deleted []string

	// UploadErr, when set, is returned by Upload and UploadIf for blobs
	// whose "container/name" starts with the key, e.g. "text-storage" or
	// "billing/accounts/".
	UploadErr map[string]error
	// BeforeUploadIf, when set, runs before every conditional upload, so a
	// test can change the blob in between (a concurrent writer).
	BeforeUploadIf func(container, name string)
}

type fakeBlob struct {
	data []byte
	etag string
}

// Put stores a blob unconditionally.
func (f *Fake) Put(container, name string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(container+"/"+name, data)
}

// Data returns a blob's content, or nil if it doesn't exist.
func (f *Fake) Data(container, name string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blobs[container+"/"+name].data
}

// Names returns the "container/name" of every blob, sorted.
func (f *Fake) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.blobs))
	for k := range f.blobs {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func (f *Fake) put(key string, data []byte) {
	if f.blobs == nil {
		f.blobs = map[string]fakeBlob{}
	}
	f.etag++
	f.blobs[key] = fakeBlob{data: data, etag: strconv.Itoa(f.etag)}
}

func (f *Fake) uploadErr(container, name string) error {
	for prefix, err := range f.UploadErr {
		if strings.HasPrefix(container+"/"+name, prefix) {
			return err
		}
	}
	return nil
}

func (f *Fake) Upload(_ context.Context, container, name string, data []byte, _ string) error {
	if err := f.uploadErr(container, name); err != nil {
		return err
	}
	f.Put(container, name, data)
	return nil
}

func (f *Fake) UploadIf(_ context.Context, container, name string, data []byte, _, ifMatch string) error {
	if err := f.uploadErr(container, name); err != nil {
		return err
	}
	if f.BeforeUploadIf != nil {
		f.BeforeUploadIf(container, name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blobs[container+"/"+name]
	if ifMatch == "" && ok || ifMatch != "" && (!ok || b.etag != ifMatch) {
		return ErrConditionNotMet
	}
	f.put(container+"/"+name, data)
	return nil
}

func (f *Fake) Get(_ context.Context, container, name string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blobs[container+"/"+name]
	if !ok {
		return nil, "", ErrNotFound
	}
	return b.data, b.etag, nil
}

func (f *Fake) List(_ context.Context, container, prefix string) ([]string, error) {
	var names []string
	for _, k := range f.Names() {
		if name, ok := strings.CutPrefix(k, container+"/"); ok && strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names, nil
}

func (f *Fake) Delete(_ context.Context, container, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Deleted = append(f.Deleted, container+"/"+name)
	if _, ok := f.blobs[container+"/"+name]; !ok {
		return ErrNotFound
	}
	delete(f.blobs, container+"/"+name)
	return nil
}

func (f *Fake) DeleteIf(_ context.Context, container, name, ifMatch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Like Blob Storage, a missing blob fails If-Match.
	if b, ok := f.blobs[container+"/"+name]; !ok || b.etag != ifMatch {
		return ErrConditionNotMet
	}
	f.Deleted = append(f.Deleted, container+"/"+name)
	delete(f.blobs, container+"/"+name)
	return nil
}
