package tokens

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
)

var (
	ErrInvalidToken = errors.New("invalid or expired token")
)

type TokenPayload struct {
	Subject   string `json:"sub"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
}

type HMACTokenManager struct {
	secret []byte
}

func NewHMACTokenManager(secret []byte) (*HMACTokenManager, error) {
	if len(secret) == 0 {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("generate secret: %w", err)
		}
		secret = buf
	}
	return &HMACTokenManager{secret: secret}, nil
}

func (m *HMACTokenManager) GenerateToken(userID string, duration time.Duration) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", errors.New("user ID required")
	}
	now := time.Now()
	payload := TokenPayload{
		Subject:   userID,
		ExpiresAt: now.Add(duration).Unix(),
		IssuedAt:  now.Unix(),
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(rawPayload)

	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(encodedPayload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return encodedPayload + "." + signature, nil
}

func (m *HMACTokenManager) ValidateToken(tokenString string) (string, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 2 {
		return "", ErrInvalidToken
	}
	encodedPayload, signature := parts[0], parts[1]

	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(encodedPayload))
	expectedSignature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expectedSignature)) {
		return "", ErrInvalidToken
	}

	rawPayload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return "", ErrInvalidToken
	}

	var payload TokenPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return "", ErrInvalidToken
	}

	if time.Now().Unix() > payload.ExpiresAt {
		return "", user.ErrUnauthorized
	}

	return payload.Subject, nil
}
