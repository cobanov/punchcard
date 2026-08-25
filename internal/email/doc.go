// Package email defines the EmailSender interface with SMTP, hosted-provider,
// and dev (log-only) adapters selected by config. Sends are made
// inline on the calling goroutine; there is no job queue.
package email
