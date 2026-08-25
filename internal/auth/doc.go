// Package auth implements the two authentication planes:
// human sessions (opaque, hashed, cookie-borne) and program access tokens
// (tdo_-prefixed PATs, SHA-256 hashed, scoped). Never mix the planes.
package auth
