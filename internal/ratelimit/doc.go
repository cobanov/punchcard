// Package ratelimit provides the in-memory token-bucket limiter behind a small
// interface so a shared store can replace it for multi-node
// deployments later. Per-IP on auth endpoints, per-principal on the API.
package ratelimit
