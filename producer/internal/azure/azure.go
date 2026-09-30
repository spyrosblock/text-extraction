// Package azure wraps the Blob Storage and Service Bus clients used by the
// producer. In Azure it authenticates with managed identity; connection
// strings are only meant for the local emulators (Azurite, Service Bus
// emulator).
package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"

	"github.com/knowledge/text-extraction/producer/internal/contract"
)

// Credential returns the credential used when no connection string is set.
// DefaultAzureCredential picks up the function app's managed identity
// (AZURE_CLIENT_ID selects a user-assigned one) or a developer login.
func Credential() (azcore.TokenCredential, error) {
	return azidentity.NewDefaultAzureCredential(nil)
}

// Blobs uploads and deletes blobs in a storage account.
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

func (b *Blobs) Delete(ctx context.Context, container, name string) error {
	_, err := b.client.DeleteBlob(ctx, container, name, nil)
	if err != nil {
		return fmt.Errorf("delete %s/%s: %w", container, name, err)
	}
	return nil
}

// Queue sends OCR requests to a Service Bus queue.
type Queue struct {
	client *azservicebus.Client
	sender *azservicebus.Sender
}

// NewQueue connects with connString when set, otherwise to the fully
// qualified namespace (e.g. "myns.servicebus.windows.net") with cred.
func NewQueue(namespace, connString, queue string, cred azcore.TokenCredential) (*Queue, error) {
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
		return nil, fmt.Errorf("service bus client: %w", err)
	}
	s, err := c.NewSender(queue, nil)
	if err != nil {
		return nil, fmt.Errorf("service bus sender: %w", err)
	}
	return &Queue{client: c, sender: s}, nil
}

func (q *Queue) Send(ctx context.Context, req contract.OCRRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	contentType := "application/json"
	err = q.sender.SendMessage(ctx, &azservicebus.Message{
		Body:        body,
		MessageID:   &req.ID,
		ContentType: &contentType,
	}, nil)
	if err != nil {
		return fmt.Errorf("send message %s: %w", req.ID, err)
	}
	return nil
}

func (q *Queue) Close(ctx context.Context) error {
	_ = q.sender.Close(ctx)
	return q.client.Close(ctx)
}
