package cdn

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// TestAESECBRoundTrip: encrypt then decrypt yields the original plaintext for
// a range of boundary sizes (0, 1, block-1, block, block+1, multi-block).
func TestAESECBRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0xa5}, 16)

	cases := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"one-byte", 1},
		{"block-minus-one", 15},
		{"exact-block", 16},
		{"block-plus-one", 17},
		{"multi-block", 1024},
		{"unaligned-large", 4096 + 7},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plaintext := bytes.Repeat([]byte{0x7a}, tc.size)
			encrypted, err := Encrypt(plaintext, key)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}
			if len(encrypted)%16 != 0 {
				t.Fatalf("ciphertext len %d not multiple of 16", len(encrypted))
			}
			decrypted, err := Decrypt(encrypted, key)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if !bytes.Equal(plaintext, decrypted) {
				t.Fatalf("round-trip mismatch: got %d bytes want %d bytes", len(decrypted), len(plaintext))
			}
		})
	}
}

// TestParseAESKey_RawBytes: 16-byte base64 payload → raw key.
func TestParseAESKey_RawBytes(t *testing.T) {
	want := bytes.Repeat([]byte{0x42}, 16)
	encoded := base64.StdEncoding.EncodeToString(want)

	got, err := ParseAESKey(encoded)
	if err != nil {
		t.Fatalf("ParseAESKey: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("key mismatch: got %x want %x", got, want)
	}
}

// TestParseAESKey_HexString: 32-byte base64 (hex-encoded key) → raw key.
func TestParseAESKey_HexString(t *testing.T) {
	raw := bytes.Repeat([]byte{0x99}, 16)
	hexKey := hex.EncodeToString(raw)
	encoded := base64.StdEncoding.EncodeToString([]byte(hexKey))

	got, err := ParseAESKey(encoded)
	if err != nil {
		t.Fatalf("ParseAESKey hex-string: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("decoded-hex key mismatch: got %x want %x", got, raw)
	}
}

// TestParseAESKey_Invalid: anything other than 16 or 32 bytes decoded length
// is rejected, and garbage base64 returns an error.
func TestParseAESKey_Invalid(t *testing.T) {
	for _, tc := range []struct {
		name, input string
	}{
		{"wrong-length", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 8))},
		{"not-base64", "!!!not-base64!!!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAESKey(tc.input); err == nil {
				t.Fatalf("expected error for %q", tc.name)
			}
		})
	}
}

// TestAESKeyEncoders: both encoders yield valid base64 that
// decodes back to the original hex string / raw bytes respectively.
func TestAESKeyEncoders(t *testing.T) {
	raw := bytes.Repeat([]byte{0xab}, 16)
	hexKey := hex.EncodeToString(raw)

	b64hex := HexStringAsBase64(hexKey)
	decoded1, err := base64.StdEncoding.DecodeString(b64hex)
	if err != nil {
		t.Fatalf("base64 decode HexStringAsBase64: %v", err)
	}
	if string(decoded1) != hexKey {
		t.Fatalf("HexStringAsBase64 round-trip: got %q want %q", decoded1, hexKey)
	}

	b64raw := HexDecodedAsBase64(hexKey)
	decoded2, err := base64.StdEncoding.DecodeString(b64raw)
	if err != nil {
		t.Fatalf("base64 decode HexDecodedAsBase64: %v", err)
	}
	if !bytes.Equal(decoded2, raw) {
		t.Fatalf("HexDecodedAsBase64 round-trip: got %x want %x", decoded2, raw)
	}
}

// TestPaddedSize: PKCS7 padding always adds at least 1 byte and pads
// to the next 16-byte multiple.
func TestPaddedSize(t *testing.T) {
	cases := []struct {
		plaintext, want int
	}{
		{0, 16},
		{1, 16},
		{15, 16},
		{16, 32},
		{17, 32},
		{31, 32},
		{32, 48},
	}
	for _, tc := range cases {
		if got := PaddedSize(tc.plaintext); got != tc.want {
			t.Fatalf("PaddedSize(%d): got %d want %d", tc.plaintext, got, tc.want)
		}
	}
}
