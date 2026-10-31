// Package azure has the producer's credential and Service Bus client. In
// Azure it authenticates with managed identity; connection strings are only
// meant for the Service Bus emulator. The blob client is in billing/blobs.
package azure

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	"github.com/spyrosblock/text-extraction/producer/internal/contract"
)

// Credential returns the credential used when no connection string is set.
// DefaultAzureCredential picks up the function app's managed identity
// (AZURE_CLIENT_ID selects a user-assigned one) or a developer login.
func Credential() (azcore.TokenCredential, error) {
	return azidentity.NewDefaultAzureCredential(nil)
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
