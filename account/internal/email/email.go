// Package email sends the magic-link emails, through Azure Communication
// Services (ACS) Email or, locally, to the log.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// Message is one email to one recipient.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// ACS sends through the ACS Email REST API with an Entra token (the function
// app's managed identity). ACS accepts the message with 202 and sends it
// asynchronously.
type ACS struct {
	// Endpoint is the communication service's, e.g.
	// https://<name>.europe.communication.azure.com.
	Endpoint string
	// Sender is the From address, e.g. DoNotReply@<guid>.azurecomm.net.
	Sender string
	Cred   azcore.TokenCredential
	// Client defaults to one with a 30 s timeout.
	Client *http.Client
}

const (
	acsAPIVersion = "2023-03-31"
	acsScope      = "https://communication.azure.com/.default"
)

var defaultClient = &http.Client{Timeout: 30 * time.Second}

type acsAddress struct {
	Address string `json:"address"`
}

type acsRequest struct {
	SenderAddress string `json:"senderAddress"`
	Content       struct {
		Subject   string `json:"subject"`
		PlainText string `json:"plainText"`
		HTML      string `json:"html"`
	} `json:"content"`
	Recipients struct {
		To []acsAddress `json:"to"`
	} `json:"recipients"`
}

func (a *ACS) Send(ctx context.Context, m Message) error {
	tok, err := a.Cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{acsScope}})
	if err != nil {
		return fmt.Errorf("acs token: %w", err)
	}
	var req acsRequest
	req.SenderAddress = a.Sender
	req.Content.Subject = m.Subject
	req.Content.PlainText = m.Text
	req.Content.HTML = m.HTML
	req.Recipients.To = []acsAddress{{Address: m.To}}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	url := strings.TrimSuffix(a.Endpoint, "/") + "/emails:send?api-version=" + acsAPIVersion
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+tok.Token)
	r.Header.Set("Content-Type", "application/json")
	client := a.Client
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(r)
	if err != nil {
		return fmt.Errorf("acs send: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("acs send: HTTP %d: %s", resp.StatusCode, respBody)
	}
	var op struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(respBody, &op)
	slog.InfoContext(ctx, "email accepted", "operationId", op.ID, "status", op.Status)
	return nil
}

// Log writes the message to the log instead of sending it. Local
// development only: it logs the sign-in link.
type Log struct{}

func (Log) Send(ctx context.Context, m Message) error {
	slog.InfoContext(ctx, "email (not sent, EMAIL_MODE=log)", "to", m.To, "subject", m.Subject, "text", m.Text)
	return nil
}
