package webhooks

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cobanov/punchcard/internal/repo"
	"github.com/cobanov/punchcard/internal/repo/db"
	"github.com/cobanov/punchcard/internal/testutil"
)

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDeliveryLifecycle(t *testing.T) {
	allowPrivateForTest = true // httptest binds to loopback
	t.Cleanup(func() { allowPrivateForTest = false })

	pool := testutil.Postgres(t)
	store := repo.NewStore(pool)
	ctx := context.Background()

	var mu sync.Mutex
	var hits int
	var lastSig string
	var lastBody []byte
	failNext := false
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		lastSig = r.Header.Get(SignatureHeaderName)
		lastBody, _ = io.ReadAll(r.Body)
		if failNext {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	// Seed a user, list, membership, webhook, and an event.
	uid := mustV7(t)
	if _, err := store.CreateUser(ctx, db.CreateUserParams{ID: uid, Email: "wh@example.com", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	lid := mustV7(t)
	if _, err := store.CreateList(ctx, db.CreateListParams{ID: lid, Name: "L", OwnerID: uid}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMember(ctx, db.AddMemberParams{ListID: lid, UserID: uid, Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	key, _ := GenerateKey()
	cipher, _ := NewCipher(key)
	const secret = "whsec_test"
	enc, _ := cipher.Encrypt([]byte(secret))
	wid := mustV7(t)
	if _, err := store.CreateWebhook(ctx, db.CreateWebhookParams{
		ID: wid, ListID: lid, Url: recv.URL, SecretEncrypted: enc, Events: []byte("[]"), CreatedBy: &uid,
	}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"type":"task.created","resource":{"id":"x"}}`)
	eid := mustV7(t)
	if _, err := store.InsertEvent(ctx, db.InsertEventParams{ID: eid, Type: "task.created", ListID: &lid, Payload: payload}); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(store, cipher, slog.New(slog.NewTextHandler(io.Discard, nil)), true, 4, 20, 0, 10)

	// Successful delivery.
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	mu.Lock()
	gotHits, gotSig, gotBody := hits, lastSig, lastBody
	mu.Unlock()
	if gotHits != 1 {
		t.Fatalf("receiver hits = %d, want 1", gotHits)
	}
	if !Verify(secret, gotSig, gotBody, 5*time.Minute, time.Now()) {
		t.Fatalf("delivered signature failed to verify (sig=%q)", gotSig)
	}
	dels, _ := store.ListDeliveriesForWebhook(ctx, db.ListDeliveriesForWebhookParams{WebhookID: wid, Limit: 10})
	if len(dels) != 1 || dels[0].Status != "success" {
		t.Fatalf("delivery not success: %+v", dels)
	}

	// Failing delivery schedules a retry.
	failNext = true
	eid2 := mustV7(t)
	if _, err := store.InsertEvent(ctx, db.InsertEventParams{ID: eid2, Type: "task.created", ListID: &lid, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce(fail): %v", err)
	}
	dels, _ = store.ListDeliveriesForWebhook(ctx, db.ListDeliveriesForWebhookParams{WebhookID: wid, Limit: 10})
	var found bool
	for _, dl := range dels {
		if dl.EventID == eid2 {
			found = true
			if dl.Status != "failed" || dl.NextRetryAt == nil {
				t.Fatalf("failed delivery not scheduled for retry: %+v", dl)
			}
		}
	}
	if !found {
		t.Fatal("no delivery recorded for the failing event")
	}
}
