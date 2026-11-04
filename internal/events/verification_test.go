package events

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestVerificationCacheBoundsAndExpiry(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	st := &Store{Now: func() time.Time { return now }}
	sub := Subscription{PrincipalKind: KindOAuth, PrincipalID: "owner", URL: "https://callback.example/private"}
	st.RememberVerification(sub, "key", now)
	now = now.Add(ChallengeMaxAge - time.Second)
	if _, ok := st.CachedVerification(sub, "key"); !ok {
		t.Fatal("valid proof was missed")
	}
	if _, ok := st.CachedVerification(sub, "rotated"); ok {
		t.Fatal("rotated key reused proof")
	}
	other := sub
	other.PrincipalID = "other"
	if _, ok := st.CachedVerification(other, "key"); ok {
		t.Fatal("another principal reused proof")
	}
	now = now.Add(time.Second)
	if _, ok := st.CachedVerification(sub, "key"); ok {
		t.Fatal("cache reads extended proof lifetime")
	}
	for i := range maxPrincipalVerifications + 10 {
		sub.URL = fmt.Sprintf("https://callback.example/%d", i)
		st.RememberVerification(sub, "key", now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(st.verifications) != maxPrincipalVerifications {
		t.Fatalf("principal cache has %d entries", len(st.verifications))
	}
	for i := range maxVerificationEntries + 10 {
		sub.PrincipalID = fmt.Sprintf("owner_%d", i)
		st.RememberVerification(sub, "key", now)
	}
	if len(st.verifications) != maxVerificationEntries {
		t.Fatalf("global cache has %d entries", len(st.verifications))
	}
}

func TestCallbackLockCancellation(t *testing.T) {
	st := &Store{}
	unlock, err := st.LockCallback(context.Background(), KindOAuth, "principal", "https://callback.example/private")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, err := st.LockCallback(ctx, KindOAuth, "principal", "https://callback.example/private"); err == nil {
		release()
		t.Fatal("cancelled waiter acquired held callback lock")
	}
}
