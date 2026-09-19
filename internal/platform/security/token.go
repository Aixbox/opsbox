package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

func NewRandomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

type TokenCipher struct {
	aead    cipher.AEAD
	hmacKey []byte
}

func NewTokenCipher(secret string) (*TokenCipher, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("create token cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create token cipher mode: %w", err)
	}
	// HMAC 密钥与加密密钥域分离：同一 secret 派生，互不干扰
	hmacKey := sha256.Sum256([]byte("hmac:" + secret))
	return &TokenCipher{aead: aead, hmacKey: hmacKey[:]}, nil
}

// Sign 计算 HMAC-SHA256 确定性签名（base64url）。同一输入永远得到同一输出：
// 用于审批预检令牌（check token）这类「无 TTL、可重复验证」的场景。
func (c *TokenCipher) Sign(data string) string {
	mac := hmac.New(sha256.New, c.hmacKey)
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifySign 以恒定时间比较校验 Sign 的签名。
func (c *TokenCipher) VerifySign(data, signature string) bool {
	if signature == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Sign(data)), []byte(signature)) == 1
}

func (c *TokenCipher) Encrypt(token string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate token nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, []byte(token), nil), nil
}

func (c *TokenCipher) Decrypt(ciphertext []byte) (string, error) {
	if len(ciphertext) < c.aead.NonceSize() {
		return "", fmt.Errorf("invalid token ciphertext")
	}
	nonce, value := ciphertext[:c.aead.NonceSize()], ciphertext[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, value, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt token: %w", err)
	}
	return string(plain), nil
}
