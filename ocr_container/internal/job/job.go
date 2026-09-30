// Package job drains pdf_queue. It runs as an event-driven Container Apps
// job execution: KEDA starts executions while the queue has messages, and
// each execution takes messages one at a time until the queue has been idle
// for a while, then exits.
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	"github.com/knowledge/text-extraction/ocr_container/internal/contract"
	"github.com/knowledge/text-extraction/ocr_container/internal/processor"
)

// Receiver is the subset of *azservicebus.Receiver the job uses.
type Receiver interface {
	ReceiveMessages(ctx context.Context, maxMessages int, options *azservicebus.ReceiveMessagesOptions) ([]*azservicebus.ReceivedMessage, error)
	CompleteMessage(ctx context.Context, msg *azservicebus.ReceivedMessage, options *azservicebus.CompleteMessageOptions) error
	AbandonMessage(ctx context.Context, msg *azservicebus.ReceivedMessage, options *azservicebus.AbandonMessageOptions) error
	DeadLetterMessage(ctx context.Context, msg *azservicebus.ReceivedMessage, options *azservicebus.DeadLetterOptions) error
	RenewMessageLock(ctx context.Context, msg *azservicebus.ReceivedMessage, options *azservicebus.RenewMessageLockOptions) error
}

type ProcessFunc func(ctx context.Context, req contract.OCRRequest) error

type Job struct {
	Receiver Receiver
	Process  ProcessFunc
	// IdleTimeout is how long to wait for a message before exiting. Zero
	// waits forever (local development).
	IdleTimeout time.Duration
	// LockRenewInterval must be shorter than the queue's lock duration;
	// OCR of a large PDF can take longer than the lock.
	LockRenewInterval time.Duration
	// MaxMessages stops the execution after this many messages. Zero means
	// no limit.
	MaxMessages int
}

// Run processes messages until the queue is idle, MaxMessages is reached or
// ctx is cancelled. Messages are received one at a time so other replicas
// can pick up the rest of the queue.
func (j *Job) Run(ctx context.Context) error {
	for n := 0; j.MaxMessages == 0 || n < j.MaxMessages; n++ {
		msg, err := j.receive(ctx)
		if err != nil || msg == nil {
			return err
		}
		j.handle(ctx, msg)
	}
	slog.InfoContext(ctx, "max messages reached", "max", j.MaxMessages)
	return nil
}

// receive returns the next message, or nil when the queue stayed idle for
// IdleTimeout or ctx was cancelled.
func (j *Job) receive(ctx context.Context) (*azservicebus.ReceivedMessage, error) {
	rctx, cancel := ctx, context.CancelFunc(func() {})
	if j.IdleTimeout > 0 {
		rctx, cancel = context.WithTimeout(ctx, j.IdleTimeout)
	}
	defer cancel()

	for {
		msgs, err := j.Receiver.ReceiveMessages(rctx, 1, nil)
		if len(msgs) > 0 {
			return msgs[0], nil
		}
		switch {
		case ctx.Err() != nil:
			slog.InfoContext(ctx, "shutting down")
			return nil, nil
		case rctx.Err() != nil:
			slog.InfoContext(ctx, "queue idle, exiting", "idle_timeout", j.IdleTimeout)
			return nil, nil
		case err != nil:
			return nil, fmt.Errorf("receive: %w", err)
		}
	}
}

func (j *Job) handle(ctx context.Context, msg *azservicebus.ReceivedMessage) {
	log := slog.With("message_id", msg.MessageID, "delivery", msg.DeliveryCount)
	// Settle even when ctx was cancelled (shutdown) so the message is
	// released right away instead of when its lock expires.
	settleCtx, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelSettle()

	var req contract.OCRRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		j.deadLetter(settleCtx, log, msg, fmt.Errorf("%w: invalid message body: %v", processor.ErrPermanent, err))
		return
	}
	log = log.With("id", req.ID)

	err := j.processWithLock(ctx, msg, req)
	switch {
	case err == nil:
		if err := j.Receiver.CompleteMessage(settleCtx, msg, nil); err != nil {
			log.ErrorContext(ctx, "complete message", "error", err)
		}
	case errors.Is(err, processor.ErrPermanent):
		j.deadLetter(settleCtx, log, msg, err)
	default:
		// Service Bus redelivers it, and dead-letters it after the queue's
		// MaxDeliveryCount.
		log.ErrorContext(ctx, "processing failed, abandoning", "error", err)
		if err := j.Receiver.AbandonMessage(settleCtx, msg, nil); err != nil {
			log.ErrorContext(ctx, "abandon message", "error", err)
		}
	}
}

// processWithLock runs Process while renewing the message lock. If the lock
// is lost the message will be redelivered, so processing is cancelled.
func (j *Job) processWithLock(ctx context.Context, msg *azservicebus.ReceivedMessage, req contract.OCRRequest) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	// Stop renewing before the message is settled.
	done := make(chan struct{})
	var wg sync.WaitGroup
	defer wg.Wait()
	defer close(done)
	if j.LockRenewInterval > 0 {
		wg.Go(func() {
			t := time.NewTicker(j.LockRenewInterval)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					return
				case <-t.C:
					if err := j.Receiver.RenewMessageLock(ctx, msg, nil); err != nil {
						cancel(fmt.Errorf("renew message lock: %w", err))
						return
					}
				}
			}
		})
	}

	err := j.Process(ctx, req)
	if cause := context.Cause(ctx); err != nil && cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return err
}

func (j *Job) deadLetter(ctx context.Context, log *slog.Logger, msg *azservicebus.ReceivedMessage, cause error) {
	log.ErrorContext(ctx, "dead-lettering message", "error", cause)
	reason := "ProcessingFailed"
	desc := cause.Error()
	if len(desc) > 1024 {
		desc = desc[:1024]
	}
	if err := j.Receiver.DeadLetterMessage(ctx, msg, &azservicebus.DeadLetterOptions{
		Reason:           &reason,
		ErrorDescription: &desc,
	}); err != nil {
		log.ErrorContext(ctx, "dead-letter message", "error", err)
	}
}
