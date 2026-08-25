package webhooks

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestSSRFValidatorBlocksMaliciousURLs(t *testing.T) {
	ctx := context.Background()
	blocked := []string{
		"http://127.0.0.1/hook",
		"https://127.0.0.1/hook",
		"https://10.0.0.5/x",
		"https://172.16.4.4/x",
		"https://192.168.1.10/x",
		"https://169.254.169.254/latest/meta-data", // cloud metadata
		"https://[::1]/x",
		"https://[fc00::1]/x",
		"https://[fe80::1]/x",
		"https://0.0.0.0/x",
		"https://localhost/x",  // resolves to loopback
		"ftp://example.com/x",  // bad scheme
		"http://example.com/x", // http not allowed by default
		"https:///nohost",
	}
	for _, u := range blocked {
		if _, err := ValidateURL(ctx, u, false); err == nil {
			t.Errorf("expected %q to be rejected, but it passed", u)
		}
	}
}

func TestSSRFValidatorAllowsPublicHTTPS(t *testing.T) {
	ctx := context.Background()
	if ips, err := ValidateURL(ctx, "https://1.1.1.1/hook", false); err != nil {
		t.Errorf("public IP literal https rejected: %v", err)
	} else if len(ips) != 1 || !ips[0].Equal(net.ParseIP("1.1.1.1")) {
		t.Errorf("unexpected pinned IPs: %v", ips)
	}
	// http allowed when self-host flag is set.
	if _, err := ValidateURL(ctx, "http://1.1.1.1/hook", true); err != nil {
		t.Errorf("http with allowHTTP rejected: %v", err)
	}
}

func TestSSRFDNSRebindingResolvedAtValidation(t *testing.T) {
	// Simulate a host that resolves to a private IP (rebinding-style): the
	// validator resolves and rejects regardless of the hostname.
	orig := resolver
	resolver = func(_ context.Context, _ string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("10.1.2.3")}}, nil
	}
	defer func() { resolver = orig }()
	if _, err := ValidateURL(context.Background(), "https://totally-legit.example.com/hook", false); err != ErrBlocked {
		t.Errorf("expected ErrBlocked for host resolving to private IP, got %v", err)
	}
}

func TestSignAndVerify(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"type":"task.created"}`)
	now := time.Now()
	header := SignatureHeader(secret, now.Unix(), body)

	if !Verify(secret, header, body, 5*time.Minute, now) {
		t.Fatal("valid signature failed to verify")
	}
	if Verify(secret, header, []byte(`{"tampered":true}`), 5*time.Minute, now) {
		t.Fatal("tampered body verified")
	}
	if Verify("wrong-secret", header, body, 5*time.Minute, now) {
		t.Fatal("wrong secret verified")
	}
	// Skew beyond tolerance.
	old := SignatureHeader(secret, now.Add(-10*time.Minute).Unix(), body)
	if Verify(secret, old, body, 5*time.Minute, now) {
		t.Fatal("stale timestamp verified")
	}
}

func TestCipherRoundTrip(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("whsec_super_secret_value")
	ct, err := c.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if string(ct) == string(plain) {
		t.Fatal("ciphertext equals plaintext")
	}
	got, err := c.Decrypt(ct)
	if err != nil || string(got) != string(plain) {
		t.Fatalf("decrypt mismatch: %q err=%v", got, err)
	}
	// Tampered ciphertext fails.
	ct[len(ct)-1] ^= 0xff
	if _, err := c.Decrypt(ct); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
	if _, err := NewCipher("short"); err == nil {
		t.Fatal("expected error for bad key")
	}
}
