package email

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type fakeCred struct{ scopes []string }

func (f *fakeCred) GetToken(_ context.Context, o policy.TokenRequestOptions) (azcore.AccessToken, error) {
	f.scopes = o.Scopes
	return azcore.AccessToken{Token: "tok", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func TestACSSend(t *testing.T) {
	var got acsRequest
	var auth, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, query = r.Header.Get("Authorization"), r.URL.RawQuery
		if r.URL.Path != "/emails:send" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"id":"op-1","status":"Running"}`))
	}))
	defer srv.Close()

	cred := &fakeCred{}
	a := &ACS{Endpoint: srv.URL + "/", Sender: "DoNotReply@x.azurecomm.net", Cred: cred}
	err := a.Send(context.Background(), Message{To: "ann@example.com", Subject: "s", Text: "t", HTML: "<p>h</p>"})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" || query != "api-version=2023-03-31" || len(cred.scopes) != 1 || cred.scopes[0] != acsScope {
		t.Errorf("auth %q, query %q, scopes %v", auth, query, cred.scopes)
	}
	if got.SenderAddress != "DoNotReply@x.azurecomm.net" || len(got.Recipients.To) != 1 || got.Recipients.To[0].Address != "ann@example.com" ||
		got.Content.Subject != "s" || got.Content.PlainText != "t" || got.Content.HTML != "<p>h</p>" {
		t.Errorf("request = %+v", got)
	}
}

func TestACSSendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"Denied"}}`, http.StatusForbidden)
	}))
	defer srv.Close()
	a := &ACS{Endpoint: srv.URL, Sender: "s", Cred: &fakeCred{}}
	if err := a.Send(context.Background(), Message{To: "ann@example.com"}); err == nil {
		t.Fatal("no error for HTTP 403")
	}
}
