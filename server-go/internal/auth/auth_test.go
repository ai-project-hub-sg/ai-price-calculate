package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// hashPassword 必须与 Python hashlib.pbkdf2_hmac("sha256", pw, salt, 120_000, 32)
// 逐字节一致——向量由 Python 生成，防双端实现悄悄漂移。
func TestHashPasswordMatchesPythonPBKDF2(t *testing.T) {
	var salt [passwordSaltBytes]byte
	for i := range salt {
		salt[i] = byte(i)
	}
	got := hashPassword(salt[:], "secret")
	const want = "9bd084e98f8bc344b0a15ec148a5b520321c80ee8bc451f5b867f8cbc26949ff"
	if hex := fmt.Sprintf("%x", got); hex != want {
		t.Fatalf("hashPassword = %s, want PBKDF2-HMAC-SHA256 %s", hex, want)
	}
}

func TestLoginAndAuthenticate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager("admin", "correct horse battery staple")
	m.now = func() time.Time { return now }

	session, err := m.Login("admin", "correct horse battery staple", "client-a")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if session.Username != "admin" {
		t.Fatalf("Username = %q, want admin", session.Username)
	}
	if session.ExpiresAt != now.Add(sessionTTL).Unix() {
		t.Fatalf("ExpiresAt = %d, want %d", session.ExpiresAt, now.Add(sessionTTL).Unix())
	}
	random, err := base64.RawURLEncoding.DecodeString(session.Token)
	if err != nil {
		t.Fatalf("token is not raw URL-safe base64: %v", err)
	}
	if len(random) != tokenBytes {
		t.Fatalf("decoded token length = %d, want %d", len(random), tokenBytes)
	}

	claims, ok := m.Authenticate(session.Token)
	if !ok {
		t.Fatal("Authenticate() rejected a new session")
	}
	wantClaims := Claims{Username: "admin", ExpiresAt: session.ExpiresAt}
	if claims != wantClaims {
		t.Fatalf("Authenticate() = %+v, want %+v", claims, wantClaims)
	}

	tokenHash := sha256.Sum256([]byte(session.Token))
	if _, stored := m.sessions[tokenHash]; !stored {
		t.Fatal("session was not stored under its SHA-256 token hash")
	}
	if len(m.sessions) != 1 {
		t.Fatalf("stored sessions = %d, want 1", len(m.sessions))
	}

	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, field := range []string{"token", "expiresAt", "username"} {
		if _, ok := fields[field]; !ok {
			t.Errorf("session JSON is missing %q", field)
		}
	}
	if len(fields) != 3 {
		t.Errorf("session JSON has %d fields, want 3", len(fields))
	}
}

func TestLoginRejectsInvalidCredentials(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
	}{
		{name: "wrong password", username: "admin", password: "wrong"},
		{name: "wrong username", username: "other", password: "secret"},
		{name: "both wrong", username: "other", password: "wrong"},
		{name: "empty credentials", username: "", password: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager("admin", "secret")
			session, err := m.Login(tt.username, tt.password, tt.name)
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
			}
			if session != (Session{}) {
				t.Fatalf("Login() session = %+v, want zero value", session)
			}
			if len(m.sessions) != 0 {
				t.Fatalf("failed login stored %d sessions", len(m.sessions))
			}
		})
	}
}

func TestNewManagerWithoutCredentialsFailsClosed(t *testing.T) {
	for _, credentials := range []Credentials{{}, {Username: "admin"}, {Password: "secret"}} {
		m := NewManager(credentials.Username, credentials.Password)
		if _, err := m.Login(credentials.Username, credentials.Password, "client"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal("Login() with incomplete manager credentials did not fail closed")
		}
	}
}

func TestAuthenticateDoesNotParseBearer(t *testing.T) {
	m := NewManager("admin", "secret")
	session, err := m.Login("admin", "secret", "client")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if claims, ok := m.Authenticate("Bearer " + session.Token); ok || claims != (Claims{}) {
		t.Fatalf("Authenticate(Bearer token) = %+v, %v; want zero claims, false", claims, ok)
	}
	if _, ok := m.Authenticate(session.Token); !ok {
		t.Fatal("Authenticate() rejected the opaque token")
	}
}

func TestExpiredSessionsAreRejectedAndCleanedUp(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager("admin", "secret")
	m.now = func() time.Time { return now }

	first, err := m.Login("admin", "secret", "client-a")
	if err != nil {
		t.Fatalf("first Login() error = %v", err)
	}
	now = now.Add(time.Hour)
	second, err := m.Login("admin", "secret", "client-b")
	if err != nil {
		t.Fatalf("second Login() error = %v", err)
	}

	now = now.Add(11 * time.Hour)
	if claims, ok := m.Authenticate(first.Token); ok || claims != (Claims{}) {
		t.Fatalf("expired Authenticate() = %+v, %v; want zero claims, false", claims, ok)
	}
	if len(m.sessions) != 1 {
		t.Fatalf("sessions after expiry cleanup = %d, want 1", len(m.sessions))
	}
	if _, ok := m.Authenticate(second.Token); !ok {
		t.Fatal("cleanup removed a live session")
	}

	now = now.Add(time.Hour)
	if _, ok := m.Authenticate(second.Token); ok {
		t.Fatal("Authenticate() accepted a session at its expiry time")
	}
	if len(m.sessions) != 0 {
		t.Fatalf("sessions after final expiry = %d, want 0", len(m.sessions))
	}
}

func TestLogoutInvalidatesOnlyItsToken(t *testing.T) {
	m := NewManager("admin", "secret")
	first, err := m.Login("admin", "secret", "client-a")
	if err != nil {
		t.Fatalf("first Login() error = %v", err)
	}
	second, err := m.Login("admin", "secret", "client-b")
	if err != nil {
		t.Fatalf("second Login() error = %v", err)
	}
	if first.Token == second.Token {
		t.Fatal("two logins returned the same token")
	}

	m.Logout("Bearer " + first.Token)
	if _, ok := m.Authenticate(first.Token); !ok {
		t.Fatal("Logout() parsed a Bearer header instead of using the opaque token")
	}
	m.Logout(first.Token)
	if _, ok := m.Authenticate(first.Token); ok {
		t.Fatal("Logout() left its token valid")
	}
	if _, ok := m.Authenticate(second.Token); !ok {
		t.Fatal("Logout() invalidated another session")
	}
	m.Logout(first.Token)
	m.Logout("unknown")
	if _, ok := m.Authenticate(second.Token); !ok {
		t.Fatal("repeated or unknown Logout() invalidated another session")
	}
	m.Logout(second.Token)
	if len(m.sessions) != 0 {
		t.Fatalf("sessions after logout = %d, want 0", len(m.sessions))
	}
}

func TestLoginRateLimitPerClient(t *testing.T) {
	m := NewManager("admin", "secret")
	for attempt := 1; attempt <= maxFailedLogins; attempt++ {
		if _, err := m.Login("admin", "wrong", "client-a"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failed attempt %d error = %v, want ErrInvalidCredentials", attempt, err)
		}
	}
	if _, err := m.Login("admin", "secret", "client-a"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Login() after limit error = %v, want ErrRateLimited", err)
	}
	if _, err := m.Login("admin", "secret", "client-b"); err != nil {
		t.Fatalf("Login() for another client error = %v", err)
	}
}

func TestSuccessfulLoginResetsFailures(t *testing.T) {
	m := NewManager("admin", "secret")
	for attempt := 0; attempt < maxFailedLogins-1; attempt++ {
		if _, err := m.Login("admin", "wrong", "client"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failed attempt %d error = %v", attempt, err)
		}
	}
	if _, err := m.Login("admin", "secret", "client"); err != nil {
		t.Fatalf("successful Login() error = %v", err)
	}
	for attempt := 1; attempt <= maxFailedLogins; attempt++ {
		if _, err := m.Login("admin", "wrong", "client"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("post-reset attempt %d error = %v, want ErrInvalidCredentials", attempt, err)
		}
	}
	if _, err := m.Login("admin", "secret", "client"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("post-reset Login() error = %v, want ErrRateLimited", err)
	}
}

func TestLoginRateLimitWindowExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewManager("admin", "secret")
	m.now = func() time.Time { return now }
	for attempt := 0; attempt < maxFailedLogins; attempt++ {
		_, _ = m.Login("admin", "wrong", "client")
	}

	now = now.Add(rateLimitWindow)
	if _, err := m.Login("admin", "secret", "client"); err != nil {
		t.Fatalf("Login() after rate-limit window error = %v", err)
	}
}

func TestNewManagerUsesRandomSalt(t *testing.T) {
	first := NewManager("admin", "secret")
	second := NewManager("admin", "secret")
	if first.salt == second.salt {
		t.Fatal("managers with the same password received the same salt")
	}
	if first.passwordHash == second.passwordHash {
		t.Fatal("managers with the same password received the same verifier")
	}
}

func TestManagerConcurrentSessions(t *testing.T) {
	m := NewManager("admin", "secret")
	const count = 16
	type result struct {
		session Session
		err     error
	}
	results := make(chan result, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, err := m.Login("admin", "secret", "client")
			results <- result{session: session, err: err}
		}()
	}
	wg.Wait()
	close(results)

	sessions := make([]Session, 0, count)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent Login() error = %v", result.err)
		}
		sessions = append(sessions, result.session)
	}
	if len(m.sessions) != count {
		t.Fatalf("concurrent sessions stored = %d, want %d", len(m.sessions), count)
	}
	for _, session := range sessions {
		if _, ok := m.Authenticate(session.Token); !ok {
			t.Fatal("concurrently created session did not authenticate")
		}
	}

	for _, session := range sessions {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			m.Logout(token)
		}(session.Token)
	}
	wg.Wait()
	if len(m.sessions) != 0 {
		t.Fatalf("sessions after concurrent logout = %d, want 0", len(m.sessions))
	}
}
