package crypto_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jaimegago/petri/pkg/crypto"
)

// testKey returns a deterministic 32-byte key for use in tests.
func testKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func TestAESCipherEncryptDecrypt(t *testing.T) {
	tests := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"short_ascii", []byte("hello")},
		{"credential_value", []byte("AKIAIOSFODNN7EXAMPLE")},
		{"unicode", []byte("日本語テスト")},
		{"binary", []byte{0x00, 0x01, 0xFE, 0xFF}},
		{"long_value", bytes.Repeat([]byte("x"), 10_000)},
		{"newlines", []byte("multi\nline\nvalue")},
		{"special_chars", []byte(`{"token":"abc123","expiry":"2025-01-01"}`)},
	}

	c, err := crypto.NewAESCipherFromKey(testKey())
	if err != nil {
		t.Fatalf("NewAESCipherFromKey: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encrypted, err := c.Encrypt(tt.plaintext)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			if encrypted == "" {
				t.Fatal("Encrypt returned empty string")
			}

			decrypted, err := c.Decrypt(encrypted)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			if !bytes.Equal(tt.plaintext, decrypted) {
				t.Errorf("round-trip mismatch\n  got:  %q\n  want: %q", decrypted, tt.plaintext)
			}
		})
	}
}

func TestAESCipherUniqueNonces(t *testing.T) {
	c, err := crypto.NewAESCipherFromKey(testKey())
	if err != nil {
		t.Fatalf("NewAESCipherFromKey: %v", err)
	}

	plaintext := []byte("identical-plaintext")
	enc1, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}
	enc2, err := c.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}

	if enc1 == enc2 {
		t.Error("two encryptions of the same plaintext produced identical ciphertext (nonces are not unique)")
	}
}

func TestAESCipherOutputIsBase64(t *testing.T) {
	c, err := crypto.NewAESCipherFromKey(testKey())
	if err != nil {
		t.Fatalf("NewAESCipherFromKey: %v", err)
	}

	encrypted, err := c.Encrypt([]byte("test"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Base64 standard encoding uses only A-Z, a-z, 0-9, +, /, =.
	for _, ch := range encrypted {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=", ch) {
			t.Errorf("encrypted output contains non-base64 character %q", ch)
		}
	}
}

func TestAESCipherDecryptInvalidInputs(t *testing.T) {
	tests := []struct {
		name       string
		ciphertext string
	}{
		{"empty_string", ""},
		{"not_base64", "!!!not-valid-base64!!!"},
		{"too_short", "aGVsbG8="}, // base64("hello") — shorter than nonce
		{"corrupted_tag", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="},
	}

	c, err := crypto.NewAESCipherFromKey(testKey())
	if err != nil {
		t.Fatalf("NewAESCipherFromKey: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.Decrypt(tt.ciphertext)
			if err == nil {
				t.Errorf("expected error for invalid ciphertext %q, got nil", tt.ciphertext)
			}
		})
	}
}

func TestAESCipherWrongKey(t *testing.T) {
	key1 := testKey()
	key2 := make([]byte, 32)
	for i := range key2 {
		key2[i] = byte(i + 100)
	}

	c1, err := crypto.NewAESCipherFromKey(key1)
	if err != nil {
		t.Fatalf("NewAESCipherFromKey key1: %v", err)
	}
	c2, err := crypto.NewAESCipherFromKey(key2)
	if err != nil {
		t.Fatalf("NewAESCipherFromKey key2: %v", err)
	}

	encrypted, err := c1.Encrypt([]byte("secret-value"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	_, err = c2.Decrypt(encrypted)
	if err == nil {
		t.Error("expected error when decrypting with wrong key, got nil")
	}
}

func TestNewAESCipherFromKeyInvalidLength(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
	}{
		{"empty", []byte{}},
		{"too_short_16", make([]byte, 16)},
		{"too_long_64", make([]byte, 64)},
		{"one_byte", make([]byte, 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := crypto.NewAESCipherFromKey(tt.key)
			if err == nil {
				t.Errorf("expected error for key of length %d, got nil", len(tt.key))
			}
		})
	}
}
