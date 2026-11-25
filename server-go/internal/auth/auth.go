package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	adminUsernameEnv = "AIPC_ADMIN_USERNAME"
	adminPasswordEnv = "AIPC_ADMIN_PASSWORD"

	passwordSaltBytes = 32
	tokenBytes        = 32
	sessionTTL        = 12 * time.Hour
	rateLimitWindow   = 15 * time.Minute
	maxFailedLogins   = 5
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrRateLimited        = errors.New("too many failed login attempts")
)

// Credentials contains the single administrator account loaded at startup.
type Credentials struct {
	Username string
	Password string
}

// Session is returned to the caller after a successful login.
type Session struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
	Username  string `json:"username"`
}

// Claims describes an authenticated in-memory session.
type Claims struct {
	Username  string
	ExpiresAt int64
}

// Manager stores a salted password verifier and hashed session tokens.
type Manager struct {
	mu sync.Mutex

	username     string
	salt         [passwordSaltBytes]byte
	passwordHash [sha256.Size]byte
	usable       bool

	sessions map[[sha256.Size]byte]Claims
	failures map[string][]time.Time
	now      func() time.Time
}

// NewManager creates an administrator manager without retaining the password.
func NewManager(username, password string) *Manager {
	m := &Manager{
		username: username,
		sessions: make(map[[sha256.Size]byte]Claims),
		failures: make(map[string][]time.Time),
		now:      time.Now,
		usable:   username != "" && password != "",
	}
	if !m.usable {
		return m
	}

	var salt [passwordSaltBytes]byte
	if _, err := rand.Read(salt[:]); err != nil {
		// The fixed API cannot report construction errors; fail closed instead.
		m.usable = false
		return m
	}
	m.salt = salt
	m.passwordHash = hashPassword(m.salt[:], password)
	return m
}

// LoadManager loads the configured administrator credentials and creates a manager.
func LoadManager() (*Manager, error) {
	credentials, err := LoadCredentials()
	if err != nil {
		return nil, err
	}
	return NewManager(credentials.Username, credentials.Password), nil
}

// Login verifies credentials, applies per-client failure limiting, and creates a session.
func (m *Manager) Login(username, password, clientKey string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	m.cleanupExpiredLocked(now)
	if len(m.recentFailuresLocked(clientKey, now)) >= maxFailedLogins {
		return Session{}, ErrRateLimited
	}

	passwordHash := hashPassword(m.salt[:], password)
	usernameMatches := subtle.ConstantTimeCompare([]byte(username), []byte(m.username)) == 1
	passwordMatches := subtle.ConstantTimeCompare(passwordHash[:], m.passwordHash[:]) == 1
	if !m.usable || !usernameMatches || !passwordMatches {
		m.failures[clientKey] = append(m.recentFailuresLocked(clientKey, now), now)
		return Session{}, ErrInvalidCredentials
	}

	delete(m.failures, clientKey)
	token, err := newSessionToken()
	if err != nil {
		return Session{}, fmt.Errorf("generate session token: %w", err)
	}

	expiresAt := now.Add(sessionTTL).Unix()
	m.sessions[sha256.Sum256([]byte(token))] = Claims{
		Username:  m.username,
		ExpiresAt: expiresAt,
	}
	return Session{Token: token, ExpiresAt: expiresAt, Username: m.username}, nil
}

// Authenticate validates an opaque token. Bearer header parsing belongs to the caller.
func (m *Manager) Authenticate(token string) (Claims, bool) {
	if token == "" {
		return Claims{}, false
	}

	tokenHash := sha256.Sum256([]byte(token))
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	m.cleanupExpiredLocked(now)
	claims, ok := m.sessions[tokenHash]
	if !ok || claims.ExpiresAt <= now.Unix() {
		delete(m.sessions, tokenHash)
		return Claims{}, false
	}
	return claims, true
}

// Logout invalidates a token. Unknown or already expired tokens are harmless.
func (m *Manager) Logout(token string) {
	if token == "" {
		return
	}

	tokenHash := sha256.Sum256([]byte(token))
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cleanupExpiredLocked(m.now())
	delete(m.sessions, tokenHash)
}

func hashPassword(salt []byte, password string) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write(salt)
	_, _ = h.Write([]byte(password))
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func newSessionToken() (string, error) {
	var random [tokenBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func (m *Manager) recentFailuresLocked(clientKey string, now time.Time) []time.Time {
	failures := m.failures[clientKey]
	cutoff := now.Add(-rateLimitWindow)
	firstCurrent := 0
	for firstCurrent < len(failures) && !failures[firstCurrent].After(cutoff) {
		firstCurrent++
	}
	if firstCurrent == 0 {
		return failures
	}
	if firstCurrent == len(failures) {
		delete(m.failures, clientKey)
		return nil
	}
	current := append([]time.Time(nil), failures[firstCurrent:]...)
	m.failures[clientKey] = current
	return current
}

func (m *Manager) cleanupExpiredLocked(now time.Time) {
	for tokenHash, claims := range m.sessions {
		if claims.ExpiresAt <= now.Unix() {
			delete(m.sessions, tokenHash)
		}
	}
}

func validateCredentials(credentials Credentials) (Credentials, error) {
	if credentials.Username == "" {
		return Credentials{}, errors.New(adminUsernameEnv + " is required")
	}
	if credentials.Password == "" {
		return Credentials{}, errors.New(adminPasswordEnv + " is required")
	}
	return credentials, nil
}
