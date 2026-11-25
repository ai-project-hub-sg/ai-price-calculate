//go:build !windows

package auth

import "os"

// LoadCredentials reads the administrator account from the environment.
func LoadCredentials() (Credentials, error) {
	return validateCredentials(Credentials{
		Username: os.Getenv(adminUsernameEnv),
		Password: os.Getenv(adminPasswordEnv),
	})
}
