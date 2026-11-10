package postgres

import (
	"testing"
)

func TestValidateProxy(t *testing.T) {
	tests := []struct {
		endpoint string
		wantErr  bool
	}{
		{"http://proxy.example.com:3128", false},
		{"socks5://198.51.100.1:1080", false},
		{"https://secure-proxy.corp:8443", false},
		{"socks5h://remote-proxy:1080", false},

		// Missing port or scheme
		{"proxy.example.com:3128", true},
		{"http://proxy.example.com", true},
		{"file:///etc/passwd", true},
		{"ftp://proxy:21", true},
		{"http://proxy:0", true},
		{"http://proxy:65536", true},

		// SSRF / loopback / metadata restrictions
		{"http://localhost:8080", true},
		{"http://127.0.0.1:8080", true},
		{"http://127.0.0.2:3128", true},
		{"http://169.254.169.254:80", true},
		{"http://[::1]:8080", true},
	}

	for _, tt := range tests {
		err := ValidateProxy(tt.endpoint)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateProxy(%q) err = %v, wantErr = %v", tt.endpoint, err, tt.wantErr)
		}
	}
}
