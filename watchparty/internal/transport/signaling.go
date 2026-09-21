// Package transport connects WebRTC data channels through a Weron signaler.
// The wire format is documented in docs/transport.md; the server only relays it.
package transport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
)

type signal struct {
	Type    string `json:"type"`
	From    string `json:"from"`
	To      string `json:"to,omitempty"`
	Payload []byte `json:"payload,omitempty"`
}

// Weron's wire format uses SHA-224 zero-padded to 32 bytes as its AES key,
// and a 12-byte nonce prepended to GCM ciphertext. Keep this for compatibility
// with the deployed signaler ecosystem, not as a password-KDF recommendation.
func signalingCipher(password string) (cipher.AEAD, error) {
	sum := sha256.Sum224([]byte(password))
	var key [32]byte
	copy(key[:], sum[:])
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	// Standard-library generation and validation of the 96-bit signaling nonce.
	// This is separate from the DTLS record nonce fixed in Pion DTLS v3.
	return cipher.NewGCMWithRandomNonce(block)
}
