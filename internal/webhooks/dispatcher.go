package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
)

// retrySchedule is the backoff between delivery attempts. After the
// last entry is exhausted the delivery is marked dead.
var retrySchedule = []time.Duration{
	1 * time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour,
}

const (
	deliveryTimeout = 10 * time.Second
	bodySnippetMax  = 1024
	leaseWindow     = 30 * time.Second
)

// Dispatcher drains the outbox and delivers webhooks. It runs as
// a single background goroutine per process; HTTP delivery fans out under a
// concurrency cap (egress control).
//
// NOTE (deliberate deviation): the original design named River for background
// jobs. To keep delivery semantics fully owned and testable, the dispatcher
// instead uses a Postgres-backed poll loop (outbox + webhook_deliveries)
// providing the same at-least-once + retry/backoff/dead-letter guarantees.
// River can replace this loop without changing the schema or delivery contract.
type Dispatcher struct {
	store            *repo.Store
	cipher           *Cipher
	log              *slog.Logger
	allowHTTP        bool
	maxConcurrency   int
	disableThreshold int
	maxWebhooks      int
	pollInterval     time.Duration
	client           *http.Client
}

// ctxKeyPinnedIP carries the SSRF-validated target IP through the request
// context so a single shared Transport can pin the dial while still pooling
// keep-alive connections per host.
type ctxKeyPinnedIP struct{}

// NewDispatcher builds a Dispatcher. A pollInterval <= 0 falls back to 2s and a
// maxWebhooks <= 0 falls back to a generous safety cap.
func NewDispatcher(store *repo.Store, cipher *Cipher, log *slog.Logger, allowHTTP bool, maxConcurrency, disableThreshold int, pollInterval time.Duration, maxWebhooks int) *Dispatcher {
	if maxConcurrency < 1 {
		maxConcurrency = 4
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	if maxWebhooks < 1 {
		maxWebhooks = 100
	}
	return &Dispatcher{
		store: store, cipher: cipher, log: log, allowHTTP: allowHTTP,
		maxConcurrency: maxConcurrency, disableThreshold: disableThreshold,
		maxWebhooks:  maxWebhooks,
		pollInterval: pollInterval,
		client:       newWebhookClient(),
	}
}

// RunOnce performs a single outbox + delivery cycle. Used by tests and callable
// for on-demand draining.
func (d *Dispatcher) RunOnce(ctx context.Context) error {
	if err := d.processOutbox(ctx); err != nil {
		return err
	}
	d.processDeliveries(ctx)
	return nil
}

// Run loops until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	t := time.NewTicker(d.pollInterval)
	defer t.Stop()
	d.log.Info("webhook dispatcher started")
	for {
		select {
		case <-ctx.Done():
			d.log.Info("webhook dispatcher stopped")
			return
		case <-t.C:
			if err := d.processOutbox(ctx); err != nil {
				d.log.ErrorContext(ctx, "outbox processing failed", "error", err.Error())
			}
			d.processDeliveries(ctx)
		}
	}
}

func subscribed(rawEvents []byte, eventType string) bool {
	var types []string
	if err := json.Unmarshal(rawEvents, &types); err != nil || len(types) == 0 {
		return true // empty subscription = all events
	}
	for _, t := range types {
		if t == eventType {
			return true
		}
	}
	return false
}

// processOutbox turns unprocessed events into pending deliveries.
func (d *Dispatcher) processOutbox(ctx context.Context) error {
	return d.store.WithTx(ctx, func(q *db.Queries) error {
		evs, err := q.ClaimUnprocessedEvents(ctx, 100)
		if err != nil {
			return err
		}
		// Memoize per-account webhooks: a batch is usually dominated by one
		// active user, so fetch each user's webhooks at most once instead of
		// once per event (N+1).
		whCache := make(map[uuid.UUID][]db.Webhook)
		for _, ev := range evs {
			whs, ok := whCache[ev.UserID]
			if !ok {
				whs, err = q.ListActiveWebhooksForUser(ctx, db.ListActiveWebhooksForUserParams{
					UserID: ev.UserID, Lim: clampI32(d.maxWebhooks),
				})
				if err != nil {
					return err
				}
				whCache[ev.UserID] = whs
			}
			for _, wh := range whs {
				if !subscribed(wh.Events, ev.Type) {
					continue
				}
				id, err := uuid.NewV7()
				if err != nil {
					return err
				}
				if _, err := q.CreateDelivery(ctx, db.CreateDeliveryParams{ID: id, WebhookID: wh.ID, EventID: ev.ID}); err != nil {
					return err
				}
			}
			if err := q.MarkEventProcessed(ctx, ev.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// processDeliveries claims due deliveries and attempts them concurrently.
func (d *Dispatcher) processDeliveries(ctx context.Context) {
	var due []db.WebhookDelivery
	err := d.store.WithTx(ctx, func(q *db.Queries) error {
		got, e := q.ClaimDueDeliveries(ctx, clampI32(d.maxConcurrency*4))
		if e != nil {
			return e
		}
		due = got
		// Lease each claimed delivery by pushing its retry time out, so an
		// overlapping tick cannot double-send while HTTP is in flight.
		lease := time.Now().Add(leaseWindow)
		for _, del := range got {
			if e := q.UpdateDeliveryResult(ctx, db.UpdateDeliveryResultParams{
				ID: del.ID, Status: del.Status, Attempt: del.Attempt,
				ResponseStatus: del.ResponseStatus, ResponseBodySnippet: del.ResponseBodySnippet,
				Error: del.Error, DeliveredAt: del.DeliveredAt, NextRetryAt: &lease,
			}); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		d.log.ErrorContext(ctx, "claiming deliveries failed", "error", err.Error())
		return
	}

	sem := make(chan struct{}, d.maxConcurrency)
	var wg sync.WaitGroup
	for _, del := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func(del db.WebhookDelivery) {
			defer wg.Done()
			defer func() { <-sem }()
			d.attempt(ctx, del)
		}(del)
	}
	wg.Wait()
}

func (d *Dispatcher) attempt(ctx context.Context, del db.WebhookDelivery) {
	wh, err := d.store.GetWebhook(ctx, del.WebhookID)
	if err != nil {
		return
	}
	ev, err := d.store.GetEvent(ctx, del.EventID)
	if err != nil {
		return
	}
	attempt := del.Attempt + 1

	if !wh.Active {
		d.finish(ctx, del, "dead", attempt, nil, "", "webhook inactive")
		return
	}
	secretBytes, err := d.cipher.Decrypt(wh.SecretEncrypted)
	if err != nil {
		d.fail(ctx, wh, del, attempt, nil, "", "secret decrypt failed")
		return
	}
	status, snippet, derr := d.send(ctx, wh.Url, string(secretBytes), ev.Payload)
	if derr == nil && status >= 200 && status < 300 {
		s := clampI32(status)
		d.finish(ctx, del, "success", attempt, &s, snippet, "")
		_ = d.store.RecordWebhookSuccess(ctx, wh.ID)
		return
	}
	msg := ""
	var sp *int32
	if derr != nil {
		msg = derr.Error()
	} else {
		s := clampI32(status)
		sp = &s
		msg = fmt.Sprintf("non-2xx status %d", status)
	}
	d.fail(ctx, wh, del, attempt, sp, snippet, msg)
}

// send performs the signed, SSRF-guarded, IP-pinned HTTP POST.
func (d *Dispatcher) send(ctx context.Context, url, secret string, payload []byte) (int, string, error) {
	ips, err := ValidateURL(ctx, url, d.allowHTTP)
	if err != nil {
		return 0, "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	// Pin the dial to the validated IP via the context; the shared client's
	// Transport reads it in DialContext (and reuses pooled conns per host).
	reqCtx = context.WithValue(reqCtx, ctxKeyPinnedIP{}, ips[0])
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	ts := time.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "punchcard-webhooks/1")
	req.Header.Set(SignatureHeaderName, SignatureHeader(secret, ts, payload))

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bodySnippetMax))
	return resp.StatusCode, string(body), nil
}

// newWebhookClient builds the single shared delivery client. It dials only the
// SSRF-validated IP carried in the request context (DNS-rebinding defense) and
// never follows redirects, but — unlike a per-request client — keeps idle
// connections so repeated deliveries to the same endpoint skip the TCP/TLS
// handshake. Idle conns are short-lived so a re-validated IP is used promptly.
func newWebhookClient() *http.Client {
	dialer := &net.Dialer{Timeout: deliveryTimeout}
	return &http.Client{
		Timeout: deliveryTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ip, ok := ctx.Value(ctxKeyPinnedIP{}).(net.IP)
				if !ok || ip == nil {
					return nil, fmt.Errorf("no validated ip pinned for %s", addr)
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			},
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     30 * time.Second,
		},
	}
}

// fail records a failed attempt, scheduling the next retry or marking the
// delivery dead once the backoff schedule is exhausted, and auto-disables the
// webhook after too many consecutive failures.
func (d *Dispatcher) fail(ctx context.Context, wh db.Webhook, del db.WebhookDelivery, attempt int32, status *int32, snippet, msg string) {
	idx := int(attempt) - 1
	if idx < len(retrySchedule) {
		// Jitter derived from the delivery id (no RNG dependency).
		jitter := time.Duration(del.ID[0]) * time.Second
		next := time.Now().Add(retrySchedule[idx] + jitter)
		_ = d.store.UpdateDeliveryResult(ctx, db.UpdateDeliveryResultParams{
			ID: del.ID, Status: "failed", Attempt: attempt, ResponseStatus: status,
			ResponseBodySnippet: ptrOrNil(snippet), Error: &msg, NextRetryAt: &next,
		})
	} else {
		d.finish(ctx, del, "dead", attempt, status, snippet, msg)
	}

	failures, err := d.store.RecordWebhookFailure(ctx, wh.ID)
	if err == nil && int(failures) >= d.disableThreshold {
		_ = d.store.DisableWebhook(ctx, db.DisableWebhookParams{
			ID: wh.ID, DisabledReason: ptrOrNil(fmt.Sprintf("auto-disabled after %d consecutive failures", failures)),
		})
	}
}

// finish records a terminal (success/dead) delivery result.
func (d *Dispatcher) finish(ctx context.Context, del db.WebhookDelivery, status string, attempt int32, respStatus *int32, snippet, errMsg string) {
	var delivered *time.Time
	if status == "success" {
		now := time.Now()
		delivered = &now
	}
	var errPtr *string
	if errMsg != "" {
		errPtr = &errMsg
	}
	_ = d.store.UpdateDeliveryResult(ctx, db.UpdateDeliveryResultParams{
		ID: del.ID, Status: status, Attempt: attempt, ResponseStatus: respStatus,
		ResponseBodySnippet: ptrOrNil(snippet), Error: errPtr, DeliveredAt: delivered, NextRetryAt: nil,
	})
}

func ptrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	if len(s) > bodySnippetMax {
		s = s[:bodySnippetMax]
	}
	return &s
}

// clampI32 converts a bounded non-negative int to int32 without overflow.
func clampI32(n int) int32 {
	if n < 0 {
		return 0
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}
