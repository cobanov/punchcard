package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "correct horse battery staple" {
		t.Fatal("password stored in plaintext")
	}

	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("verify correct: ok=%v err=%v", ok, err)
	}

	ok, err = VerifyPassword("wrong password", hash)
	if err != nil {
		t.Fatalf("verify wrong returned error: %v", err)
	}
	if ok {
		t.Fatal("verify accepted a wrong password")
	}
}

func TestHashPasswordUniqueSalts(t *testing.T) {
	h1, _ := HashPassword("same")
	h2, _ := HashPassword("same")
	if h1 == h2 {
		t.Fatal("identical hashes for same password: salt not random")
	}
}

func TestVerifyPasswordRejectsGarbage(t *testing.T) {
	if _, err := VerifyPassword("x", "not-a-phc-string"); err == nil {
		t.Fatal("expected error for malformed hash")
	}
}

func TestPATFormat(t *testing.T) {
	full, prefix, err := NewPAT()
	if err != nil {
		t.Fatalf("NewPAT: %v", err)
	}
	if len(full) < 20 || full[:4] != PATPrefix {
		t.Fatalf("unexpected token format: %q", full)
	}
	if prefix != full[:12] {
		t.Fatalf("prefix mismatch: %q vs %q", prefix, full[:12])
	}
	if a := HashToken(full); len(a) != 32 {
		t.Fatalf("hash length = %d, want 32", len(a))
	}
}
