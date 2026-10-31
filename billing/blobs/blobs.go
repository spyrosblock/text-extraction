// Package blobs wraps the Blob Storage client used by the producer and the
// account app. In Azure it authenticates with managed identity; connection
// strings are only meant for the local emulator (Azurite).
package blobs

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
)

var (
	// ErrNotFound is returned when the blob does not exist.
	ErrNotFound = errors.New("blob not found")
	// ErrConditionNotMet is returned by UploadIf and DeleteIf when the blob
	// changed (or, for a new blob, already exists).
	ErrConditionNotMet = errors.New("blob condition not met")
)

// Blobs reads, uploads and deletes blobs in a storage account.
type Blobs struct {
	client *azblob.Client
}

// NewBlobs connects with connString when set, otherwise to accountURL with cred.
func NewBlobs(accountURL, connString string, cred azcore.TokenCredential) (*Blobs, error) {
	var (
		c   *azblob.Client
		err error
	)
	if connString != "" {
		c, err = azblob.NewClientFromConnectionString(connString, nil)
	} else {
		c, err = azblob.NewClient(accountURL, cred, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("blob client: %w", err)
	}
	return &Blobs{client: c}, nil
}

// EnsureContainer creates the container if it does not exist. Meant for local
// development; in Azure the containers are provisioned with the infrastructure.
func (b *Blobs) EnsureContainer(ctx context.Context, container string) error {
	_, err := b.client.CreateContainer(ctx, container, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
		return fmt.Errorf("create container %s: %w", container, err)
	}
	return nil
}

func (b *Blobs) Upload(ctx context.Context, container, name string, data []byte, contentType string) error {
	_, err := b.client.UploadBuffer(ctx, container, name, data, &azblob.UploadBufferOptions{
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: &contentType},
	})
	if err != nil {
		return fmt.Errorf("upload %s/%s: %w", container, name, err)
	}
	return nil
}

// UploadIf uploads data only if the blob's ETag is still ifMatch. An empty
// ifMatch means the blob must not exist yet.
func (b *Blobs) UploadIf(ctx context.Context, container, name string, data []byte, contentType, ifMatch string) error {
	cond := &blob.ModifiedAccessConditions{}
	if ifMatch == "" {
		cond.IfNoneMatch = to(azcore.ETagAny)
	} else {
		cond.IfMatch = to(azcore.ETag(ifMatch))
	}
	_, err := b.client.UploadBuffer(ctx, container, name, data, &azblob.UploadBufferOptions{
		HTTPHeaders:      &blob.HTTPHeaders{BlobContentType: &contentType},
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: cond},
	})
	if bloberror.HasCode(err, bloberror.ConditionNotMet, bloberror.BlobAlreadyExists) {
		return fmt.Errorf("upload %s/%s: %w", container, name, ErrConditionNotMet)
	}
	if err != nil {
		return fmt.Errorf("upload %s/%s: %w", container, name, err)
	}
	return nil
}

// Get returns a blob's content and ETag.
func (b *Blobs) Get(ctx context.Context, container, name string) ([]byte, string, error) {
	resp, err := b.client.DownloadStream(ctx, container, name, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
		return nil, "", fmt.Errorf("download %s/%s: %w", container, name, ErrNotFound)
	}
	if err != nil {
		return nil, "", fmt.Errorf("download %s/%s: %w", container, name, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("download %s/%s: %w", container, name, err)
	}
	var etag string
	if resp.ETag != nil {
		etag = string(*resp.ETag)
	}
	return data, etag, nil
}

// List returns the names of the blobs in container that start with prefix.
func (b *Blobs) List(ctx context.Context, container, prefix string) ([]string, error) {
	var names []string
	pager := b.client.NewListBlobsFlatPager(container, &azblob.ListBlobsFlatOptions{Prefix: &prefix})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list %s/%s: %w", container, prefix, err)
		}
		for _, item := range page.Segment.BlobItems {
			names = append(names, *item.Name)
		}
	}
	return names, nil
}

func (b *Blobs) Delete(ctx context.Context, container, name string) error {
	_, err := b.client.DeleteBlob(ctx, container, name, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
		return fmt.Errorf("delete %s/%s: %w", container, name, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("delete %s/%s: %w", container, name, err)
	}
	return nil
}

// DeleteIf deletes the blob only if its ETag is still ifMatch, so of two
// callers holding the same ETag only one succeeds. A blob that is gone
// fails the condition too (ErrConditionNotMet, HTTP 412).
func (b *Blobs) DeleteIf(ctx context.Context, container, name, ifMatch string) error {
	_, err := b.client.DeleteBlob(ctx, container, name, &azblob.DeleteBlobOptions{
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{
			IfMatch: to(azcore.ETag(ifMatch)),
		}},
	})
	if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
		return fmt.Errorf("delete %s/%s: %w", container, name, ErrNotFound)
	}
	if bloberror.HasCode(err, bloberror.ConditionNotMet) {
		return fmt.Errorf("delete %s/%s: %w", container, name, ErrConditionNotMet)
	}
	if err != nil {
		return fmt.Errorf("delete %s/%s: %w", container, name, err)
	}
	return nil
}

func to[T any](v T) *T { return &v }
