package main

/*
	Session auth: form login at /login, random 32-byte hex token in the
	kvmostb_session cookie (HttpOnly, SameSite=Lax, 12h fixed TTL).

	Fail-closed: if ADMIN_USER or ADMIN_PASSWORD is unset or empty, login
	always returns 401. Credentials are read from the environment on each
	login attempt.

	Limitations (LAN appliance): sessions live in memory and are lost on
	restart, the TTL never slides, and login attempts are not rate limited.
	Add a persistent store and a per-IP throttle before exposing the box to
	the internet.
*/

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"kvmostb/internal/logging"
)

const (
	sessionCookieName = "kvmostb_session"
	sessionTTL        = 12 * time.Hour
)

var sessions = newSessionStore()

// sessionStore maps token -> expiry.
type sessionStore struct {
	mu     sync.Mutex
	expiry map[string]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{expiry: make(map[string]time.Time)}
}

// newSession mints a random 32-byte hex token valid for sessionTTL.
func (s *sessionStore) newSession() (string, time.Time) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure means the token is not secure: abort.
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	token := hex.EncodeToString(buf)
	expires := time.Now().Add(sessionTTL)
	s.mu.Lock()
	s.cleanupLocked()
	s.expiry[token] = expires
	s.mu.Unlock()
	return token, expires
}

// valid reports whether the token exists and is not expired. Expired
// entries are cleaned up lazily on every access.
func (s *sessionStore) valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	exp, ok := s.expiry[token]
	return ok && time.Now().Before(exp)
}

func (s *sessionStore) delete(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	delete(s.expiry, token)
	s.mu.Unlock()
}

// cleanupLocked drops expired entries (caller holds s.mu).
func (s *sessionStore) cleanupLocked() {
	now := time.Now()
	for tok, exp := range s.expiry {
		if now.After(exp) {
			delete(s.expiry, tok)
		}
	}
}

// The Secure flag is omitted because the console is served over plain HTTP;
// set it if TLS is ever terminated.

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true, // JS must never read the token
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   0,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// handleLogin: POST /api/login with JSON {"username","password"}.
// Public route; credentials come from the environment (see header).
func handleLogin(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("POST required"))
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body"))
		return
	}
	user := os.Getenv("ADMIN_USER")
	pass := os.Getenv("ADMIN_PASSWORD")
	// Fail-closed: no credentials configured -> login always 401.
	if user == "" || pass == "" ||
		subtle.ConstantTimeCompare([]byte(body.Username), []byte(user)) != 1 ||
		subtle.ConstantTimeCompare([]byte(body.Password), []byte(pass)) != 1 {
		// Log the failure without echoing username or password.
		logging.Warnf("login failed: invalid credentials from %s", req.RemoteAddr)
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"ok":    false,
			"error": "invalid credentials",
		})
		return
	}
	token, _ := sessions.newSession()
	setSessionCookie(w, token)
	logging.Infof("login: new session from %s", req.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleLogout: POST /api/logout (auth required by sessionAuth).
// Deletes the session token and clears the cookie.
func handleLogout(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("POST required"))
		return
	}
	if c, err := req.Cookie(sessionCookieName); err == nil {
		sessions.delete(c.Value)
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// serveLoginPage serves the embedded login form (public route).
func serveLoginPage(w http.ResponseWriter, _ *http.Request) {
	data, err := fs.ReadFile(webFS, "web/login.html")
	if err != nil {
		http.Error(w, "login page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// sessionAuth requires a valid session cookie except on public paths. API and
// WebSocket routes get 401 without one; HTML routes redirect to /login.
func sessionAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if publicPath(req.URL.Path) {
			next.ServeHTTP(w, req)
			return
		}
		if c, err := req.Cookie(sessionCookieName); err == nil && sessions.valid(c.Value) {
			next.ServeHTTP(w, req)
			return
		}
		if req.URL.Path == "/ws/hid" || strings.HasPrefix(req.URL.Path, "/api/") {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("unauthorized"))
			return
		}
		http.Redirect(w, req, "/login", http.StatusFound)
	})
}

// publicPath lists routes reachable without a session. The static assets are
// public because the login page must load them before sign-in.
func publicPath(p string) bool {
	switch p {
	case "/login", "/login.html", "/shared-css.css", "/api/login",
		"/kvmostb.webp", "/kvmostb.png", "/favicon.ico":
		return true
	}
	return strings.HasPrefix(p, "/favicon/")
}
