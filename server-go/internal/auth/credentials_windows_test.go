//go:build windows

package auth

import (
	"syscall"
	"testing"
)

func TestLoadCredentialsPrefersEnvironment(t *testing.T) {
	t.Setenv(adminUsernameEnv, "admin")
	t.Setenv(adminPasswordEnv, "secret")

	credentials, err := LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials() error = %v", err)
	}
	if credentials.Username != "admin" || credentials.Password != "secret" {
		t.Fatal("LoadCredentials() did not use the environment values")
	}
}

func TestLoadCredentialsRejectsPartialEnvironment(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
	}{
		{name: "missing username", password: "secret"},
		{name: "missing password", username: "admin"},
		{name: "empty username", password: "secret"},
		{name: "empty password", username: "admin"},
		{name: "both empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(adminUsernameEnv, tt.username)
			t.Setenv(adminPasswordEnv, tt.password)
			if _, err := LoadCredentials(); err == nil {
				t.Fatal("LoadCredentials() accepted incomplete environment credentials")
			}
		})
	}
}

func TestUTF16PointerString(t *testing.T) {
	const username = "管理员"
	pointer, err := syscall.UTF16PtrFromString(username)
	if err != nil {
		t.Fatalf("UTF16PtrFromString() error = %v", err)
	}
	if got := utf16PointerString(pointer); got != username {
		t.Fatalf("utf16PointerString() = %q, want %q", got, username)
	}
	if got := utf16PointerString(nil); got != "" {
		t.Fatalf("utf16PointerString(nil) = %q, want empty string", got)
	}
}
