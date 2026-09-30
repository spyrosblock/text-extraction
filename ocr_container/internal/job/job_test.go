package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	"github.com/knowledge/text-extraction/ocr_container/internal/contract"
	"github.com/knowledge/text-extraction/ocr_container/internal/processor"
)

type fakeReceiver struct {
	mu       sync.Mutex
	queue    []*azservicebus.ReceivedMessage
	settled  map[string]string // message id -> complete | abandon | deadletter
	renewals int
	renewErr error
}

func newReceiver(bodies ...string) *fakeReceiver {
	r := &fakeReceiver{settled: map[string]string{}}
	for i, b := range bodies {
		r.queue = append(r.queue, &azservicebus.ReceivedMessage{MessageID: fmt.Sprint(i), Body: []byte(b)})
	}
	return r
}

func (r *fakeReceiver) ReceiveMessages(ctx context.Context, _ int, _ *azservicebus.ReceiveMessagesOptions) ([]*azservicebus.ReceivedMessage, error) {
	r.mu.Lock()
	if len(r.queue) > 0 {
		m := r.queue[0]
		r.queue = r.queue[1:]
		r.mu.Unlock()
		return []*azservicebus.ReceivedMessage{m}, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (r *fakeReceiver) settle(m *azservicebus.ReceivedMessage, how string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settled[m.MessageID] = how
	return nil
}

func (r *fakeReceiver) CompleteMessage(_ context.Context, m *azservicebus.ReceivedMessage, _ *azservicebus.CompleteMessageOptions) error {
	return r.settle(m, "complete")
}

func (r *fakeReceiver) AbandonMessage(_ context.Context, m *azservicebus.ReceivedMessage, _ *azservicebus.AbandonMessageOptions) error {
	return r.settle(m, "abandon")
}

func (r *fakeReceiver) DeadLetterMessage(_ context.Context, m *azservicebus.ReceivedMessage, _ *azservicebus.DeadLetterOptions) error {
	return r.settle(m, "deadletter")
}

func (r *fakeReceiver) RenewMessageLock(context.Context, *azservicebus.ReceivedMessage, *azservicebus.RenewMessageLockOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.renewals++
	return r.renewErr
}

func msg(id string) string { return fmt.Sprintf(`{"id":%q,"blob":%q}`, id, id+".pdf") }

func TestRunSettlesAndExitsWhenIdle(t *testing.T) {
	r := newReceiver(msg("ok"), msg("transient"), msg("permanent"), "not json")
	var processed []string
	j := &Job{
		Receiver:    r,
		IdleTimeout: 50 * time.Millisecond,
		Process: func(_ context.Context, req contract.OCRRequest) error {
			processed = append(processed, req.ID)
			switch req.ID {
			case "transient":
				return errors.New("blob 503")
			case "permanent":
				return fmt.Errorf("%w: bad pdf", processor.ErrPermanent)
			}
			return nil
		},
	}
	if err := j.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"0": "complete", "1": "abandon", "2": "deadletter", "3": "deadletter"}
	for id, how := range want {
		if r.settled[id] != how {
			t.Errorf("message %s settled %q, want %q", id, r.settled[id], how)
		}
	}
	if len(processed) != 3 {
		t.Errorf("processed = %v", processed)
	}
}

func TestRunMaxMessages(t *testing.T) {
	r := newReceiver(msg("a"), msg("b"), msg("c"))
	j := &Job{Receiver: r, MaxMessages: 2, Process: func(context.Context, contract.OCRRequest) error { return nil }}
	if err := j.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.settled) != 2 || len(r.queue) != 1 {
		t.Fatalf("settled = %v, left = %d", r.settled, len(r.queue))
	}
}

func TestRunShutdownAbandons(t *testing.T) {
	r := newReceiver(msg("slow"))
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{Receiver: r, Process: func(ctx context.Context, _ contract.OCRRequest) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}}
	if err := j.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if r.settled["0"] != "abandon" {
		t.Fatalf("settled = %v, want abandon", r.settled)
	}
}

func TestLockRenewal(t *testing.T) {
	r := newReceiver(msg("long"))
	j := &Job{
		Receiver:          r,
		IdleTimeout:       10 * time.Millisecond,
		LockRenewInterval: 5 * time.Millisecond,
		Process: func(context.Context, contract.OCRRequest) error {
			time.Sleep(40 * time.Millisecond)
			return nil
		},
	}
	if err := j.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.renewals < 2 || r.settled["0"] != "complete" {
		t.Fatalf("renewals = %d settled = %v", r.renewals, r.settled)
	}
}

func TestLockLostCancelsProcessing(t *testing.T) {
	r := newReceiver(msg("long"))
	r.renewErr = errors.New("lock lost")
	j := &Job{
		Receiver:          r,
		IdleTimeout:       10 * time.Millisecond,
		LockRenewInterval: 5 * time.Millisecond,
		Process: func(ctx context.Context, _ contract.OCRRequest) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return nil
			}
		},
	}
	if err := j.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.settled["0"] != "abandon" {
		t.Fatalf("settled = %v, want abandon", r.settled)
	}
}
