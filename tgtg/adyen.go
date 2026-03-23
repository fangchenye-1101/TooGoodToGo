package tgtg

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"time"
)

const (
	adyenPrefix  = "adyenjs"
	adyenVersion = "_0_1_1"

	adyenKeyURL = "https://checkoutshopper-live.adyen.com/checkoutshopper/v1/clientKeys/live_VPX45BIMLFAIVARYVKEDNC7OXIFBRQZ5"

	ccmNonceLen = 12
	ccmTagLen   = 8
	aesKeyLen   = 32 // 256-bit
)

// AdyenEncryptor encrypts card fields for Adyen payment processing.
type AdyenEncryptor struct {
	publicKey *rsa.PublicKey
}

// NewAdyenEncryptor fetches the Adyen live public key and returns an encryptor.
func NewAdyenEncryptor(session *Session) (*AdyenEncryptor, error) {
	_, body, err := session.Get(adyenKeyURL)
	if err != nil {
		return nil, fmt.Errorf("fetch adyen key: %w", err)
	}

	var resp struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse adyen key response: %w", err)
	}

	pub, err := parseAdyenPublicKey(resp.PublicKey)
	if err != nil {
		return nil, err
	}

	return &AdyenEncryptor{publicKey: pub}, nil
}

// EncryptCard encrypts all four card fields and returns the encrypted values.
func (e *AdyenEncryptor) EncryptCard(card CardData) (encCard, encCVV, encMonth, encYear string, err error) {
	encCard, err = e.encryptField("number", card.Number)
	if err != nil {
		return
	}
	encCVV, err = e.encryptField("cvc", card.CVV)
	if err != nil {
		return
	}
	encMonth, err = e.encryptField("expiryMonth", card.Month)
	if err != nil {
		return
	}
	encYear, err = e.encryptField("expiryYear", card.Year)
	return
}

func (e *AdyenEncryptor) encryptField(name, value string) (string, error) {
	genTime := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	plainObj := map[string]string{
		name:             value,
		"generationtime": genTime,
	}
	plainJSON, err := json.Marshal(plainObj)
	if err != nil {
		return "", err
	}

	aesKey := make([]byte, aesKeyLen)
	if _, err := rand.Read(aesKey); err != nil {
		return "", fmt.Errorf("generate AES key: %w", err)
	}

	nonce := make([]byte, ccmNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext, err := ccmEncrypt(aesKey, nonce, plainJSON, ccmTagLen)
	if err != nil {
		return "", fmt.Errorf("AES-CCM encrypt: %w", err)
	}

	encAESKey, err := rsa.EncryptPKCS1v15(rand.Reader, e.publicKey, aesKey)
	if err != nil {
		return "", fmt.Errorf("RSA encrypt: %w", err)
	}

	// nonce is prepended to ciphertext, matching the Python reference
	encCardComponent := append(nonce, ciphertext...)

	return fmt.Sprintf("%s%s$%s$%s",
		adyenPrefix,
		adyenVersion,
		base64.StdEncoding.EncodeToString(encAESKey),
		base64.StdEncoding.EncodeToString(encCardComponent),
	), nil
}

// BuildPayOrderPayload constructs the full payment request body.
func (e *AdyenEncryptor) BuildPayOrderPayload(card CardData) (*PayOrderRequest, error) {
	encCard, encCVV, encMonth, encYear, err := e.EncryptCard(card)
	if err != nil {
		return nil, err
	}

	innerPayload := map[string]any{
		"type":                   "scheme",
		"encryptedCardNumber":    encCard,
		"encryptedExpiryMonth":   encMonth,
		"encryptedExpiryYear":    encYear,
		"encryptedSecurityCode":  encCVV,
		"threeDS2SdkVersion":     "2.2.10",
	}
	payloadBytes, err := json.Marshal(innerPayload)
	if err != nil {
		return nil, err
	}

	return &PayOrderRequest{
		Authorization: PaymentAuthorization{
			AuthorizationPayload: AdyenAuthPayload{
				Payload:           string(payloadBytes),
				PaymentType:       "CREDITCARD",
				SavePaymentMethod: false,
				Type:              "adyenAuthorizationPayload",
			},
			PaymentProvider: "ADYEN",
			ReturnURL:       "adyencheckout://com.app.tgtg.itemview",
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Adyen public key parsing  ("10001|ABCDEF...")
// ---------------------------------------------------------------------------

func parseAdyenPublicKey(encoded string) (*rsa.PublicKey, error) {
	parts := splitTwo(encoded, "|")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid adyen public key format")
	}

	e := new(big.Int)
	e.SetString(parts[0], 16)

	n := new(big.Int)
	n.SetString(parts[1], 16)

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

func splitTwo(s, sep string) []string {
	idx := 0
	for i := range s {
		if string(s[i]) == sep {
			return []string{s[:i], s[i+1:]}
		}
		idx = i
	}
	_ = idx
	return []string{s}
}

// ---------------------------------------------------------------------------
// AES-CCM (RFC 3610) — encrypt-only implementation
// ---------------------------------------------------------------------------

func ccmEncrypt(key, nonce, plaintext []byte, tagLen int) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(nonce) < 7 || len(nonce) > 13 {
		return nil, fmt.Errorf("ccm: invalid nonce length %d", len(nonce))
	}

	q := 15 - len(nonce) // length field size
	pLen := len(plaintext)

	// --- CBC-MAC (authentication tag) ---
	flags0 := byte((tagLen-2)/2) << 3 // (t-2)/2 in bits [5:3]
	flags0 |= byte(q - 1)             // q-1 in bits [2:0]

	b0 := make([]byte, aes.BlockSize)
	b0[0] = flags0
	copy(b0[1:], nonce)
	// encode plaintext length in the last q bytes (big-endian)
	for i := 0; i < q; i++ {
		b0[15-i] = byte(pLen >> (8 * i))
	}

	tag := cbcMAC(block, b0, plaintext)

	// --- CTR encryption ---
	flagsCtr := byte(q - 1)
	a0 := make([]byte, aes.BlockSize)
	a0[0] = flagsCtr
	copy(a0[1:], nonce)
	// A_0 counter = 0 (already zeroed)

	// Encrypt tag with A_0
	s0 := make([]byte, aes.BlockSize)
	block.Encrypt(s0, a0)
	encTag := xorBytes(tag[:tagLen], s0[:tagLen])

	// Encrypt plaintext with A_1, A_2, ...
	ctr := make([]byte, aes.BlockSize)
	copy(ctr, a0)
	encData := make([]byte, pLen)
	for i := 0; i < pLen; i += aes.BlockSize {
		incrementCounter(ctr, q)
		sBlock := make([]byte, aes.BlockSize)
		block.Encrypt(sBlock, ctr)
		end := i + aes.BlockSize
		if end > pLen {
			end = pLen
		}
		copy(encData[i:end], xorBytes(plaintext[i:end], sBlock[:end-i]))
	}

	// ciphertext = encrypted_data || encrypted_tag
	result := make([]byte, 0, len(encData)+len(encTag))
	result = append(result, encData...)
	result = append(result, encTag...)
	return result, nil
}

func cbcMAC(block cipher.Block, b0 []byte, data []byte) []byte {
	x := make([]byte, aes.BlockSize)
	block.Encrypt(x, b0)

	// process plaintext in 16-byte blocks
	padded := pkcs7Pad(data, aes.BlockSize)
	for i := 0; i < len(padded); i += aes.BlockSize {
		blk := padded[i : i+aes.BlockSize]
		xored := xorBytes(x, blk)
		block.Encrypt(x, xored)
	}
	return x
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	if len(data) == 0 {
		return data
	}
	rem := len(data) % blockSize
	if rem == 0 {
		return data
	}
	padLen := blockSize - rem
	padded := make([]byte, len(data)+padLen)
	copy(padded, data)
	return padded
}

func incrementCounter(ctr []byte, q int) {
	// increment the last q bytes as a big-endian integer
	for i := len(ctr) - 1; i >= len(ctr)-q; i-- {
		ctr[i]++
		if ctr[i] != 0 {
			break
		}
	}
}

func xorBytes(a, b []byte) []byte {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = a[i] ^ b[i]
	}
	return out
}