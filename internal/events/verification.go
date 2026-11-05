package events

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"
)

const maxVerificationEntries = 1024
const maxPrincipalVerifications = 100

type verificationKey struct {
	principal [32]byte
	callback  [32]byte
	secret    [32]byte
}

func callbackKey(kind, principal, callback string) verificationKey {
	identity, _ := json.Marshal([]string{kind, principal})
	return verificationKey{principal: sha256.Sum256(identity), callback: sha256.Sum256([]byte(callback))}
}

// LockCallback serializes subscribe and unsubscribe through challenge activation.
// Fixed shards bound lock memory, while cancellation stops queued requests.
// URL and credential material never enter diagnostics or a retained lock map.
func (st *Store) LockCallback(ctx context.Context, kind, principal, callback string) (func(), error) {
	st.callbackOnce.Do(func() {
		for i := range st.callbackLocks {
			st.callbackLocks[i] = make(chan struct{}, 1)
		}
	})
	key := callbackKey(kind, principal, callback)
	lane := st.callbackLocks[int(key.principal[0]^key.callback[0])%len(st.callbackLocks)]
	select {
	case lane <- struct{}{}:
		return func() { <-lane }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// CachedVerification reuses a successful callback proof only for the same
// principal, URL and signing key. Reads never extend its five minute lifetime.
func (st *Store) CachedVerification(s Subscription, secret string) (time.Time, bool) {
	key := callbackKey(s.PrincipalKind, s.PrincipalID, s.URL)
	key.secret = sha256.Sum256([]byte(secret))
	st.verificationMu.Lock()
	defer st.verificationMu.Unlock()
	at, ok := st.verifications[key]
	return at, ok && at.After(st.now().Add(-ChallengeMaxAge))
}

// RememberVerification retains hashes only, with hard global and per-principal
// entry bounds. Expired entries are removed on insertion; oldest proofs go first.
func (st *Store) RememberVerification(s Subscription, secret string, at time.Time) {
	key := callbackKey(s.PrincipalKind, s.PrincipalID, s.URL)
	key.secret = sha256.Sum256([]byte(secret))
	st.verificationMu.Lock()
	defer st.verificationMu.Unlock()
	if st.verifications == nil {
		st.verifications = make(map[verificationKey]time.Time)
	}
	for k, verified := range st.verifications {
		if !verified.After(st.now().Add(-ChallengeMaxAge)) {
			delete(st.verifications, k)
		}
	}
	st.verifications[key] = at
	for {
		count := 0
		var oldestKey, principalOldestKey verificationKey
		var oldest, principalOldest time.Time
		for k, verified := range st.verifications {
			if oldest.IsZero() || verified.Before(oldest) {
				oldestKey, oldest = k, verified
			}
			if k.principal == key.principal {
				count++
				if principalOldest.IsZero() || verified.Before(principalOldest) {
					principalOldestKey, principalOldest = k, verified
				}
			}
		}
		if count > maxPrincipalVerifications {
			delete(st.verifications, principalOldestKey)
			continue
		}
		if len(st.verifications) > maxVerificationEntries {
			delete(st.verifications, oldestKey)
			continue
		}
		return
	}
}
