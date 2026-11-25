//go:build !windows

package auth

import (
	"strings"
	"testing"
)

func TestLoadCredentialsFromEnvironment(t *testing.T) {
	t.Setenv(adminUsernameEnv, "admin")
	t.Setenv(adminPasswordEnv, "  secret with spaces  ")

	credentials, err := LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials() error = %v", err)
	}
	if credentials.Username != "admin" || credentials.Password != "  secret with spaces  " {
		t.Fatal("LoadCredentials() did not preserve the environment values")
	}
}

func TestLoadCredentialsRequiresBothEnvironmentVariables(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
		missing  string
	}{
		{name: "missing username", password: "secret", missing: adminUsernameEnv},
		{name: "missing password", username: "admin", missing: adminPasswordEnv},
		{name: "empty username", password: "secret", missing: adminUsernameEnv},
		{name: "empty password", username: "admin", missing: adminPasswordEnv},
		{name: "both missing", missing: adminUsernameEnv},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(adminUsernameEnv, tt.username)
			t.Setenv(adminPasswordEnv, tt.password)
			credentials, err := LoadCredentials()
			if err == nil {
				t.Fatalf("LoadCredentials() = %+v, nil; want an error", credentials)
			}
			if !strings.Contains(err.Error(), tt.missing) {
				t.Fatalf("LoadCredentials() error = %v, want mention of %s", err, tt.missing)
			}
		})
	}
}

func TestLoadManagerWithoutEnvironmentFails(t *testing.T) {
	t.Setenv(adminUsernameEnv, "")
	t.Setenv(adminPasswordEnv, "")
	manager, err := LoadManager()
	if err == nil {
		t.Fatalf("LoadManager() returned no error without credentials")
	}
	if manager != nil {
		t.Fatal("LoadManager() returned a manager without credentials")
	}
}

func TestLoadManagerWithEnvironment(t *testing.T) {
	t.Setenv(adminUsernameEnv, "admin")
	t.Setenv(adminPasswordEnv, "secret")
	manager, err := LoadManager()
	if err != nil {
		t.Fatalf("LoadManager() error = %v", err)
	}
	if _, err := manager.Login("admin", "secret", "client"); err != nil {
		t.Fatalf("Login() with loaded manager error = %v", err)
	}
}
