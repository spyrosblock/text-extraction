// Package azure wraps the Blob Storage client used by the consumer. In Azure
// it authenticates with managed identity; a connection string is only meant
// for the local emulator (Azurite).
package azure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
)

// ErrNotFound is returned by Blobs.Open when the blob does not exist.
var ErrNotFound = errors.New("blob not found")

// Credential returns the credential used when no connection string is set.
// DefaultAzureCredential picks up the function app's managed identity
// (AZURE_CLIENT_ID selects a user-assigned one) or a developer login.
func Credential() (azcore.TokenCredential, error) {
	return azidentity.NewDefaultAzureCredential(nil)
}

// Blobs reads blobs from a storage account.
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

// Open streams a blob. The caller must close the returned reader.
// lastModified is when the blob was written.
func (b *Blobs) Open(ctx context.Context, container, name string) (body io.ReadCloser, lastModified time.Time, err error) {
	resp, err := b.client.DownloadStream(ctx, container, name, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
		return nil, time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("download %s/%s: %w", container, name, err)
	}
	if resp.LastModified != nil {
		lastModified = *resp.LastModified
	}
	return resp.Body, lastModified, nil
}
