// Package webhooks handles per-list webhook registration, HMAC signing, SSRF
// defense (resolve-and-pin, private-range rejection), delivery with retry/
// backoff, and egress caps.
package webhooks
