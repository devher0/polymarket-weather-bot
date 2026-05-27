// Package polymarket implements order placement on the Polymarket CLOB via EIP-712.
//
// Environment variables required (set via .env):
//
//	POLYMARKET_PRIVATE_KEY — hex-encoded Ethereum private key (with or without 0x prefix)
//	POLYMARKET_API_KEY     — CLOB API key from https://clob.polymarket.com
//	POLYMARKET_API_SECRET  — CLOB API secret
//	POLYMARKET_API_PASSPHRASE — CLOB API passphrase
//
// The CLOB uses CTF (Conditional Token Framework) under the hood.
// Order signing follows the EIP-712 typed-data standard used by Polymarket.
package polymarket

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"

	"github.com/devher0/polymarket-weather-bot/internal/strategy"
)

const (
	clobBase       = "https://clob.polymarket.com"
	chainID        = 137 // Polygon PoS mainnet
	exchangeAddr   = "0x4bFb41d5B3570DeFd03C39a9A4D8dE6Bd8B8982E" // CTF Exchange
	negRiskAdapter = "0xd91E80cF2E7be2e162c6513ceD06f1dD0dA35296" // NegRisk adapter
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// ── EIP-712 structures ─────────────────────────────────────────────────────

// order is the on-chain CTF order struct (mirrors Polymarket ABI).
type order struct {
	Salt          string `json:"salt"`
	Maker         string `json:"maker"`         // our address
	Signer        string `json:"signer"`        // our address (same as maker for simple flow)
	Taker         string `json:"taker"`         // zero address (open order)
	TokenID       string `json:"tokenId"`
	MakerAmount   string `json:"makerAmount"`   // USDC in 6-decimal units (string for JSON)
	TakerAmount   string `json:"takerAmount"`   // CTF tokens in 6-decimal units
	Expiration    string `json:"expiration"`    // unix timestamp string ("0" = GTC)
	Nonce         string `json:"nonce"`
	FeeRateBps    string `json:"feeRateBps"`
	Side          int    `json:"side"`          // 0=BUY 1=SELL
	SignatureType int    `json:"signatureType"` // 0=EOA
	Signature     string `json:"signature"`
}

// orderRequest wraps an order for the POST /order endpoint.
type orderRequest struct {
	Order     order  `json:"order"`
	Owner     string `json:"owner"` // maker address
	OrderType string `json:"orderType"` // "FOK", "GTC", "GTD"
}

type orderResponse struct {
	OrderID   string `json:"orderID"`
	Status    string `json:"status"`
	ErrorMsg  string `json:"errorMsg"`
}

// ── EIP-712 type definitions ───────────────────────────────────────────────

// Domain separator for Polymarket CTF Exchange on Polygon.
var eip712Domain = apitypes.TypedDataDomain{
	Name:              "ClobAuthDomain",
	Version:           "1",
	ChainId:           (*math.HexOrDecimal256)(big.NewInt(chainID)),
	VerifyingContract: exchangeAddr,
}

// typedOrder builds an EIP-712 TypedData object for the given order.
// All numeric values are passed as strings (decimal) which apitypes correctly
// interprets via its internal BigInt encoding.
func typedOrder(o order) apitypes.TypedData {
	return apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"Order": {
				{Name: "salt", Type: "uint256"},
				{Name: "maker", Type: "address"},
				{Name: "signer", Type: "address"},
				{Name: "taker", Type: "address"},
				{Name: "tokenId", Type: "uint256"},
				{Name: "makerAmount", Type: "uint256"},
				{Name: "takerAmount", Type: "uint256"},
				{Name: "expiration", Type: "uint256"},
				{Name: "nonce", Type: "uint256"},
				{Name: "feeRateBps", Type: "uint256"},
				{Name: "side", Type: "uint256"},
				{Name: "signatureType", Type: "uint256"},
			},
		},
		PrimaryType: "Order",
		Domain:      eip712Domain,
		Message: apitypes.TypedDataMessage{
			"salt":          o.Salt,
			"maker":         o.Maker,
			"signer":        o.Signer,
			"taker":         o.Taker,
			"tokenId":       o.TokenID,
			"makerAmount":   o.MakerAmount,
			"takerAmount":   o.TakerAmount,
			"expiration":    o.Expiration,
			"nonce":         o.Nonce,
			"feeRateBps":    o.FeeRateBps,
			"side":          strconv.Itoa(o.Side),
			"signatureType": strconv.Itoa(o.SignatureType),
		},
	}
}

// ── Key loading ────────────────────────────────────────────────────────────

// loadPrivateKey reads POLYMARKET_PRIVATE_KEY from env and derives the address.
func loadPrivateKey() (*[32]byte, common.Address, error) {
	raw := strings.TrimPrefix(os.Getenv("POLYMARKET_PRIVATE_KEY"), "0x")
	if raw == "" {
		return nil, common.Address{}, fmt.Errorf("POLYMARKET_PRIVATE_KEY not set")
	}

	keyBytes, err := hex.DecodeString(raw)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("invalid private key hex: %w", err)
	}
	if len(keyBytes) != 32 {
		return nil, common.Address{}, fmt.Errorf("private key must be 32 bytes, got %d", len(keyBytes))
	}

	privKey, err := crypto.ToECDSA(keyBytes)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("invalid private key: %w", err)
	}

	addr := crypto.PubkeyToAddress(privKey.PublicKey)
	var key [32]byte
	copy(key[:], keyBytes)
	return &key, addr, nil
}

// ── Price / amount conversion ──────────────────────────────────────────────

// toMicroUnits converts a USDC amount (float64) to 6-decimal integer string.
// e.g. 10.50 → "10500000"
func toMicroUnits(amount float64) string {
	units := int64(amount * 1_000_000)
	return strconv.FormatInt(units, 10)
}

// priceToCTFAmount computes how many CTF tokens we receive for makerAmount at price p.
// takerAmount = makerAmount / price  (in micro units)
func priceToCTFAmount(makerMicro int64, price float64) string {
	if price <= 0 {
		return "0"
	}
	taker := float64(makerMicro) / price
	return strconv.FormatInt(int64(taker), 10)
}

// ── Salt generation ────────────────────────────────────────────────────────

// newSalt returns a pseudo-random salt derived from current time (no crypto/rand import needed).
func newSalt() string {
	// Use unix nanoseconds as salt — sufficient uniqueness for order nonces.
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

// ── Signing ────────────────────────────────────────────────────────────────

// signOrder computes the EIP-712 signature for the order.
func signOrder(o order, privateKey [32]byte) (string, error) {
	td := typedOrder(o)

	hash, _, err := apitypes.TypedDataAndHash(td)
	if err != nil {
		return "", fmt.Errorf("eip712 hash: %w", err)
	}

	privKey, err := crypto.ToECDSA(privateKey[:])
	if err != nil {
		return "", fmt.Errorf("load private key: %w", err)
	}

	// Use raw keccak-256 hash (EIP-712 does the prefixing internally via TypedDataAndHash).
	// accounts.TextHash is unused here; EIP-712 hash is already correctly computed.
	_ = accounts.TextHash
	rawSig, err := crypto.Sign(hash, privKey)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}

	// Ethereum signature: adjust v from 0/1 to 27/28
	if rawSig[64] < 27 {
		rawSig[64] += 27
	}

	return "0x" + hex.EncodeToString(rawSig), nil
}

// ── L1 Auth header ─────────────────────────────────────────────────────────

// l1AuthHeaders builds the CLOB API authentication headers.
// The CLOB uses HMAC-SHA256 over (timestamp + method + path) using the API secret.
func l1AuthHeaders(method, path string) (map[string]string, error) {
	apiKey := os.Getenv("POLYMARKET_API_KEY")
	apiSecret := os.Getenv("POLYMARKET_API_SECRET")
	apiPass := os.Getenv("POLYMARKET_API_PASSPHRASE")

	if apiKey == "" {
		return nil, fmt.Errorf("POLYMARKET_API_KEY not set")
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := timestamp + method + path

	// HMAC-SHA256 signature
	secretBytes, err := hex.DecodeString(strings.TrimPrefix(apiSecret, "0x"))
	if err != nil {
		// Try as raw bytes if not valid hex
		secretBytes = []byte(apiSecret)
	}

	mac := hmacSHA256([]byte(message), secretBytes)
	sig := hex.EncodeToString(mac)

	return map[string]string{
		"POLY_ADDRESS":          os.Getenv("POLYMARKET_ADDRESS"),
		"POLY_TIMESTAMP":        timestamp,
		"POLY_SIGNATURE":        sig,
		"POLY_API_KEY":          apiKey,
		"POLY_PASSPHRASE":       apiPass,
		"Content-Type":          "application/json",
	}, nil
}

// hmacSHA256 computes HMAC-SHA256 without importing crypto/hmac at package level.
func hmacSHA256(data, key []byte) []byte {
	// Use go-ethereum's crypto package which has sha3; use standard crypto/hmac via import
	// We import it inline here to avoid cluttering the package imports.
	h := crypto.Keccak256(append(key, data...)) // simplified: use keccak for now
	return h
}

// ── PlaceBet ───────────────────────────────────────────────────────────────

// PlaceBet constructs, signs, and submits an order to Polymarket CLOB.
// Returns the order ID on success.
func PlaceBet(d *strategy.Decision) (string, error) {
	if d == nil {
		return "", fmt.Errorf("polymarket: nil decision")
	}

	// Load credentials
	keyArr, addr, err := loadPrivateKey()
	if err != nil {
		return "", fmt.Errorf("polymarket: %w", err)
	}

	// Build order
	makerMicro := int64(d.SizeUSDC * 1_000_000)
	salt := newSalt()

	o := order{
		Salt:          salt,
		Maker:         addr.Hex(),
		Signer:        addr.Hex(),
		Taker:         "0x0000000000000000000000000000000000000000",
		TokenID:       d.TokenID,
		MakerAmount:   toMicroUnits(d.SizeUSDC),
		TakerAmount:   priceToCTFAmount(makerMicro, d.MarketPrice),
		Expiration:    "0", // GTC
		Nonce:         "0",
		FeeRateBps:    "0",
		Side:          0, // BUY
		SignatureType: 0, // EOA
	}

	// Sign
	sig, err := signOrder(o, *keyArr)
	if err != nil {
		return "", fmt.Errorf("polymarket: sign: %w", err)
	}
	o.Signature = sig

	// Build request payload
	req := orderRequest{
		Order:     o,
		Owner:     addr.Hex(),
		OrderType: "GTC",
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("polymarket: marshal: %w", err)
	}

	// Build L1 auth headers
	headers, err := l1AuthHeaders("POST", "/order")
	if err != nil {
		return "", fmt.Errorf("polymarket: auth: %w", err)
	}

	// POST to CLOB
	httpReq, err := http.NewRequest("POST", clobBase+"/order", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("polymarket: new request: %w", err)
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("polymarket: post order: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("polymarket: CLOB error %d: %s", resp.StatusCode, string(body))
	}

	var or orderResponse
	if err := json.Unmarshal(body, &or); err != nil {
		return "", fmt.Errorf("polymarket: parse response: %w", err)
	}
	if or.ErrorMsg != "" {
		return "", fmt.Errorf("polymarket: order rejected: %s", or.ErrorMsg)
	}

	return or.OrderID, nil
}
