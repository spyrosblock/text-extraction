// Package blobtest is an in-memory stand-in for azure.Blobs in unit tests.
package blobtest

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/spyrosblock/text-extraction/producer/internal/azure"
)

// Fake keeps blobs in memory under "container/name". It is safe for
// concurrent use.
type Fake struct {
	mu      sync.Mutex
	blobs   map[string]blob
	etag    int
	Deleted []string

	// UploadErr, when set, is returned by Upload for blobs in that container.
	UploadErr map[string]error
	// BeforeUploadIf, when set, runs before every conditional upload, so a
	// test can change the blob in between (a concurrent writer).
	BeforeUploadIf func(container, name string)
}

type blob struct {
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
		f.blobs = map[string]blob{}
	}
	f.etag++
	f.blobs[key] = blob{data: data, etag: strconv.Itoa(f.etag)}
}

func (f *Fake) Upload(_ context.Context, container, name string, data []byte, _ string) error {
	if err := f.UploadErr[container]; err != nil {
		return err
	}
	f.Put(container, name, data)
	return nil
}

func (f *Fake) UploadIf(_ context.Context, container, name string, data []byte, _, ifMatch string) error {
	if f.BeforeUploadIf != nil {
		f.BeforeUploadIf(container, name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blobs[container+"/"+name]
	if ifMatch == "" && ok || ifMatch != "" && (!ok || b.etag != ifMatch) {
		return azure.ErrConditionNotMet
	}
	f.put(container+"/"+name, data)
	return nil
}

func (f *Fake) Get(_ context.Context, container, name string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blobs[container+"/"+name]
	if !ok {
		return nil, "", azure.ErrNotFound
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
		return azure.ErrNotFound
	}
	delete(f.blobs, container+"/"+name)
	return nil
}
