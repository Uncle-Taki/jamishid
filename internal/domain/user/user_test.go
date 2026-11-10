package user

import (
	"strings"
	"testing"
)

func TestUserValidationAndNormalization(t *testing.T) {
	t.Run("NormalizeIdentifier", func(t *testing.T) {
		if got := NormalizeIdentifier("  User@Example.COM  "); got != "user@example.com" {
			t.Fatalf("expected user@example.com, got %s", got)
		}
	})

	t.Run("ValidateIdentifier", func(t *testing.T) {
		if err := ValidateIdentifier("ab"); err != ErrInvalidIdentifier {
			t.Fatalf("expected ErrInvalidIdentifier for short string, got %v", err)
		}
		if err := ValidateIdentifier(strings.Repeat("a", 255)); err != ErrInvalidIdentifier {
			t.Fatalf("expected ErrInvalidIdentifier for long string, got %v", err)
		}
		if err := ValidateIdentifier("valid_user"); err != nil {
			t.Fatalf("expected nil error for valid_user, got %v", err)
		}
	})

	t.Run("ValidatePassword", func(t *testing.T) {
		if err := ValidatePassword("12345"); err != ErrInvalidPassword {
			t.Fatalf("expected ErrInvalidPassword for short password, got %v", err)
		}
		if err := ValidatePassword(strings.Repeat("p", 73)); err != ErrInvalidPassword {
			t.Fatalf("expected ErrInvalidPassword for long password, got %v", err)
		}
		if err := ValidatePassword("secret123"); err != nil {
			t.Fatalf("expected nil for valid password, got %v", err)
		}
	})
}
