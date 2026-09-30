// Package azure wraps the Blob Storage and Service Bus clients used by the
// ocr_container. In Azure it authenticates with managed identity; connection
// strings are only meant for the local emulators (Azurite, Service Bus
// emulator).
package azure

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
)

// ErrNotFound is returned when a blob does not exist.
var ErrNotFound = errors.New("blob not found")

// Credential returns the credential used when no connection string is set.
// DefaultAzureCredential picks up the job's managed identity
// (AZURE_CLIENT_ID selects a user-assigned one) or a developer login.
func Credential() (azcore.TokenCredential, error) {
	return azidentity.NewDefaultAzureCredential(nil)
}

// Blobs reads, writes and deletes blobs in a storage account.
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

// Download returns the blob's content, or ErrNotFound.
func (b *Blobs) Download(ctx context.Context, container, name string) ([]byte, error) {
	resp, err := b.client.DownloadStream(ctx, container, name, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("download %s/%s: %w", container, name, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if resp.ContentLength != nil {
		buf.Grow(int(*resp.ContentLength))
	}
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, fmt.Errorf("download %s/%s: %w", container, name, err)
	}
	return buf.Bytes(), nil
}

// Exists reports whether the blob exists.
func (b *Blobs) Exists(ctx context.Context, container, name string) (bool, error) {
	_, err := b.client.ServiceClient().NewContainerClient(container).NewBlobClient(name).GetProperties(ctx, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get properties %s/%s: %w", container, name, err)
	}
	return true, nil
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

// Delete deletes the blob. A blob that is already gone is not an error.
func (b *Blobs) Delete(ctx context.Context, container, name string) error {
	_, err := b.client.DeleteBlob(ctx, container, name, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.BlobNotFound) {
		return fmt.Errorf("delete %s/%s: %w", container, name, err)
	}
	return nil
}

// NewReceiver returns a peek-lock receiver for queue, connecting with
// connString when set, otherwise to the fully qualified namespace (e.g.
// "myns.servicebus.windows.net") with cred. Closing the receiver does not
// close the client; close both.
func NewReceiver(namespace, connString, queue string, cred azcore.TokenCredential) (*azservicebus.Client, *azservicebus.Receiver, error) {
	var (
		c   *azservicebus.Client
		err error
	)
	if connString != "" {
		c, err = azservicebus.NewClientFromConnectionString(connString, nil)
	} else {
		c, err = azservicebus.NewClient(namespace, cred, nil)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("service bus client: %w", err)
	}
	r, err := c.NewReceiverForQueue(queue, nil)
	if err != nil {
		_ = c.Close(context.Background())
		return nil, nil, fmt.Errorf("service bus receiver: %w", err)
	}
	return c, r, nil
}
