package hub

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Secrets at rest that the hub must use in the clear later (Grok Bot
// routine keys, OAuth material, Events callback secrets) are sealed with
// AES-256-GCM under meta.hub_key (connectors plan Global Constraints).

// sealPrefix versions the sealed format: "v1:" + base64(nonce|ciphertext).
const sealPrefix = "v1:"

// ErrUnseal is returned for any sealed value that does not decrypt: wrong
// format or version, truncated, tampered with, or sealed by another hub.
var ErrUnseal = errors.New("hub: sealed value does not open")

// sealInfo separates the sealing key from the other uses of meta.hub_key
// (blob read tokens are HMACs under the raw key).
const sealInfo = "vecnet seal v1"

// sealAEAD returns AES-256-GCM keyed by HMAC-SHA256(meta.hub_key, sealInfo).
func (h *Hub) sealAEAD() (cipher.AEAD, error) {
	if h.Blobs == nil || len(h.Blobs.Key) < 32 {
		return nil, errors.New("hub: meta.hub_key is not loaded")
	}
	mac := hmac.New(sha256.New, h.Blobs.Key)
	mac.Write([]byte(sealInfo))
	block, err := aes.NewCipher(mac.Sum(nil)) // 32 bytes: AES-256
	if err != nil {
		return nil, fmt.Errorf("hub: seal: %w", err)
	}
	return cipher.NewGCM(block) // 12-byte nonce, 16-byte tag
}

// Seal encrypts plaintext with AES-256-GCM under meta.hub_key and a random
// 12-byte nonce, returning "v1:" + base64(nonce|ciphertext). The result is
// safe to store; it never needs to appear in a log.
func (h *Hub) Seal(plaintext []byte) (string, error) {
	aead, err := h.sealAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("hub: seal: %w", err)
	}
	out := aead.Seal(nonce, nonce, plaintext, nil)
	return sealPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// Unseal reverses Seal. Anything that fails to authenticate is ErrUnseal.
func (h *Hub) Unseal(sealed string) ([]byte, error) {
	aead, err := h.sealAEAD()
	if err != nil {
		return nil, err
	}
	rest, ok := strings.CutPrefix(sealed, sealPrefix)
	if !ok {
		return nil, ErrUnseal
	}
	raw, err := base64.StdEncoding.DecodeString(rest)
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrUnseal
	}
	n := aead.NonceSize()
	plain, err := aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return nil, ErrUnseal
	}
	if plain == nil {
		plain = []byte{}
	}
	return plain, nil
}
