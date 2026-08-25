// Package events implements the transactional outbox: every domain
// mutation writes an event row in the same transaction. Fan-out to webhooks and
// SSE is by polling that table (WEBHOOK_POLL_INTERVAL, SSE_POLL_INTERVAL), not
// LISTEN/NOTIFY — a deliberate deviation with the same delivery and resume
// semantics.
package events
