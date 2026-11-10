package tokens

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
)

func TestHMACTokenManager_Lifecycle(t *testing.T) {
	tm, err := NewHMACTokenManager([]byte("super-secret-key-1234567890123456"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	token, err := tm.GenerateToken("user-123", time.Hour)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	subject, err := tm.ValidateToken(token)
	if err != nil {
		t.Fatalf("failed to validate token: %v", err)
	}
	if subject != "user-123" {
		t.Errorf("expected subject user-123, got %s", subject)
	}

	// Tampered token
	tampered := token[:len(token)-4] + "abcd"
	if _, err := tm.ValidateToken(tampered); err == nil {
		t.Errorf("expected validation failure for tampered token")
	}

	// Expired token
	expiringTM, _ := NewHMACTokenManager([]byte("super-secret-key-1234567890123456"))
	expiredToken, _ := expiringTM.GenerateToken("user-123", -time.Second)
	if _, err := expiringTM.ValidateToken(expiredToken); err != user.ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized for expired token, got %v", err)
	}

	// Nil secret creates random key
	randomTM, err := NewHMACTokenManager(nil)
	if err != nil {
		t.Fatalf("unexpected error with nil secret: %v", err)
	}
	rToken, err := randomTM.GenerateToken("rand-user", time.Minute)
	if err != nil {
		t.Fatalf("failed to generate with random key: %v", err)
	}
	if sub, err := randomTM.ValidateToken(rToken); err != nil || sub != "rand-user" {
		t.Fatalf("failed to validate token with random key: %v, sub: %s", err, sub)
	}

	// Empty user ID error
	if _, err := tm.GenerateToken("", time.Hour); err == nil {
		t.Errorf("expected error for empty user ID")
	}

	// Malformed tokens
	if _, err := tm.ValidateToken("invalidtoken"); err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken for token without dot")
	}
	if _, err := tm.ValidateToken("not-b64-payload.invalid-sig"); err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken for invalid signature")
	}
	// Valid signature but invalid base64 payload
	macPayload := "not_base64_payload???"
	// Valid base64 but invalid JSON payload
	invalidJSONPayload := "bm90LWpzb24" // RawURLEncoding of "not-json"
	// Create valid signature for invalid json payload:
	rawInvToken, _ := tm.GenerateToken("placeholder", time.Hour)
	_ = rawInvToken
	// Tamper payload to be invalid json with its signature
	mac := HMACTokenManager{secret: tm.secret}
	// Sign invalid base64
	sigBadB64 := mac.sign(macPayload)
	if _, err := tm.ValidateToken(macPayload + "." + sigBadB64); err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken for bad base64 payload")
	}
	// Sign invalid json
	sigBadJSON := mac.sign(invalidJSONPayload)
	if _, err := tm.ValidateToken(invalidJSONPayload + "." + sigBadJSON); err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken for invalid JSON payload")
	}
}

func (m *HMACTokenManager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
