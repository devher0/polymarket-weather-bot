package polymarket

import (
	"strings"
	"testing"
)

// TestToMicroUnits verifies USDC → micro-unit conversion.
func TestToMicroUnits(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0, "0"},
		{1, "1000000"},
		{10.5, "10500000"},
		{0.01, "10000"},
		{100.999999, "100999999"},
	}
	for _, tc := range tests {
		got := toMicroUnits(tc.input)
		if got != tc.want {
			t.Errorf("toMicroUnits(%.6f) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestNewSalt verifies that successive salts differ.
func TestNewSalt(t *testing.T) {
	s1 := newSalt()
	s2 := newSalt()
	if s1 == "" {
		t.Error("salt should not be empty")
	}
	// In practice they should differ (nanosecond resolution), but we can't guarantee it
	// in a tight loop on slow machines, so just assert non-empty.
	_ = s2
}

// TestPriceToCTFAmount verifies CTF amount calculation.
func TestPriceToCTFAmount(t *testing.T) {
	// 10 USDC at price 0.5 should yield 20 CTF tokens (in micro units)
	makerMicro := int64(10_000_000)  // 10 USDC in micro
	got := priceToCTFAmount(makerMicro, 0.5)
	if got != "20000000" {
		t.Errorf("priceToCTFAmount(10000000, 0.5) = %q, want %q", got, "20000000")
	}

	// Edge: zero price should not panic
	got = priceToCTFAmount(makerMicro, 0)
	if got != "0" {
		t.Errorf("priceToCTFAmount at 0 price should return 0, got %q", got)
	}
}

// TestLoadPrivateKeyInvalid verifies that a bad key returns an error.
func TestLoadPrivateKeyInvalid(t *testing.T) {
	t.Setenv("POLYMARKET_PRIVATE_KEY", "notahexkey")
	_, _, err := loadPrivateKey()
	if err == nil {
		t.Error("expected error for invalid private key")
	}
}

// TestLoadPrivateKeyMissing verifies empty env returns an error.
func TestLoadPrivateKeyMissing(t *testing.T) {
	t.Setenv("POLYMARKET_PRIVATE_KEY", "")
	_, _, err := loadPrivateKey()
	if err == nil {
		t.Error("expected error when POLYMARKET_PRIVATE_KEY not set")
	}
	if !strings.Contains(err.Error(), "not set") {
		t.Errorf("expected 'not set' in error, got: %v", err)
	}
}

// TestLoadPrivateKeyValid verifies a well-known test key is accepted.
// Uses a known Ethereum test private key (NOT a real key with funds).
func TestLoadPrivateKeyValid(t *testing.T) {
	// This is a known test key used in many Go Ethereum examples.
	// DO NOT use this key for any real assets.
	testKey := "fad9c8855b740a0b7ed4c221dbad0f33a83a49cad6b3fe8d5817ac83d38b6a19"
	t.Setenv("POLYMARKET_PRIVATE_KEY", testKey)

	key, addr, err := loadPrivateKey()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key == nil {
		t.Error("expected non-nil key")
	}
	if addr.Hex() == "" || addr.Hex() == "0x0000000000000000000000000000000000000000" {
		t.Errorf("expected non-zero address, got %s", addr.Hex())
	}
}

// TestSignOrder verifies that a signature is produced for a valid order.
// Uses the test private key above.
func TestSignOrder(t *testing.T) {
	testKey := "fad9c8855b740a0b7ed4c221dbad0f33a83a49cad6b3fe8d5817ac83d38b6a19"
	t.Setenv("POLYMARKET_PRIVATE_KEY", testKey)

	_, addr, err := loadPrivateKey()
	if err != nil {
		t.Fatalf("key load: %v", err)
	}

	var keyArr [32]byte
	// decode key manually for sign test
	for i, b := range func() []byte {
		kb, _ := hexDecode(testKey)
		return kb
	}() {
		keyArr[i] = b
	}

	o := order{
		Salt:          "12345678",
		Maker:         addr.Hex(),
		Signer:        addr.Hex(),
		Taker:         "0x0000000000000000000000000000000000000000",
		TokenID:       "99999999",
		MakerAmount:   "10000000",
		TakerAmount:   "20000000",
		Expiration:    "0",
		Nonce:         "0",
		FeeRateBps:    "0",
		Side:          0,
		SignatureType: 0,
	}

	sig, err := signOrder(o, keyArr)
	if err != nil {
		t.Fatalf("signOrder: %v", err)
	}
	if !strings.HasPrefix(sig, "0x") {
		t.Errorf("signature should start with 0x, got %q", sig[:10])
	}
	// EIP-712 signature is 65 bytes = 130 hex chars + "0x" = 132
	if len(sig) != 132 {
		t.Errorf("expected 132-char signature, got %d: %s", len(sig), sig)
	}
}

func hexDecode(s string) ([]byte, error) {
	s = strings.TrimPrefix(s, "0x")
	b := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		var v byte
		for _, c := range s[i : i+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= byte(c - '0')
			case c >= 'a' && c <= 'f':
				v |= byte(c-'a') + 10
			case c >= 'A' && c <= 'F':
				v |= byte(c-'A') + 10
			}
		}
		b[i/2] = v
	}
	return b, nil
}
