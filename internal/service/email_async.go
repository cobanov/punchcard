package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/cobanov/punchcard/internal/email"
)

// emailSendTimeout bounds one delivery attempt. An SMTP host that accepts the
// connection and then stalls would otherwise hold a slot indefinitely.
const emailSendTimeout = 30 * time.Second

// maxConcurrentSends caps simultaneous outbound SMTP conversations. Goroutines
// are cheap; sockets and the relay's connection limit are not. Acquisition
// blocks rather than dropping, so a burst is delayed, never silently discarded.
var emailSlots = make(chan struct{}, 8)

// sendAsync delivers m off the caller's goroutine.
//
// Every caller of this already treats delivery as best-effort — all three sites
// only logged a failure and carried on — so moving the wait off the request
// changes no status code and no response body. What it changes is two things
// that mattered:
//
// Latency: POST /v1/auth/password-reset/request used to take as long as a full
// SMTP conversation, and a stalled mail host stalled the request with it.
//
// Account enumeration: RequestPasswordReset returns early and sends nothing when
// the address has no account, but sent — and waited — when it did. The handler
// replies identically either way, deliberately, so the endpoint cannot be used
// to test whether an address is registered. The wait leaked exactly that: an
// existing address took an SMTP round-trip, a non-existent one returned at once.
// Both branches now return in the same time.
//
// The context is detached from the request with WithoutCancel so the send is not
// cancelled the moment the handler returns, while keeping any request-scoped
// values (the request id) attached to the log line.
//
// In-flight sends are not awaited at shutdown. That is the same failure mode as
// an SMTP host being down — the user does not get the mail and retries — and it
// is why this is acceptable for best-effort mail and would not be for anything
// the user is told has definitely happened.
func sendAsync(ctx context.Context, sender email.Sender, log *slog.Logger, m email.Message) {
	ctx = context.WithoutCancel(ctx)
	go func() {
		emailSlots <- struct{}{}
		defer func() { <-emailSlots }()

		ctx, cancel := context.WithTimeout(ctx, emailSendTimeout)
		defer cancel()

		if err := sender.Send(ctx, m); err != nil {
			// Deliberately no recipient in the log line: these are exactly the
			// addresses the enumeration defence above exists to protect, and a
			// log is a much easier thing to read than a timing side channel.
			log.WarnContext(ctx, "email send failed", "error", err, "subject", m.Subject)
		}
	}()
}
