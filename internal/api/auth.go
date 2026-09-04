package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// sessionCookie holds the signed session token. The __Host- prefix is
	// deliberately NOT used: it requires Secure, and this app is normally
	// served over plain HTTP inside the office network.
	sessionCookie = "se2026_session"

	// sessionTTL is how long one login lasts. Long enough that a day of
	// work doesn't get interrupted, short enough that a forgotten open
	// browser doesn't stay authenticated for weeks.
	sessionTTL = 12 * time.Hour
)

// auth holds the single account the dashboard accepts, plus the key its
// session cookies are signed with.
type auth struct {
	username string
	password string
	key      []byte
}

// newAuth derives the cookie-signing key from the credentials themselves.
// That has a useful property: changing AUTH_PASSWORD in .env invalidates
// every existing session automatically, with no extra secret to manage and
// no session store to keep. It gives away nothing — anyone who knows the
// password can log in anyway, so being able to derive the key from it is
// not a further loss.
//
// The alternative, a random key generated at startup, would log everyone
// out on every deploy; a key in .env would be one more secret to rotate.
func newAuth(username, password string) *auth {
	sum := sha256.Sum256([]byte("se2026-titik-maps/session/v1\x00" + username + "\x00" + password))
	return &auth{username: username, password: password, key: sum[:]}
}

// check verifies a submitted username/password pair. Both comparisons are
// constant-time so a wrong username can't be distinguished from a wrong
// password by timing, and neither leaks how many leading characters were
// right.
func (a *auth) check(username, password string) bool {
	u := subtle.ConstantTimeCompare([]byte(username), []byte(a.username))
	p := subtle.ConstantTimeCompare([]byte(password), []byte(a.password))
	return u == 1 && p == 1
}

// issue writes a fresh session cookie. The token is "<expiry unix>.<hmac>"
// — stateless, so nothing has to be remembered across restarts, and
// unforgeable without the key.
func (a *auth) issue(w http.ResponseWriter, r *http.Request) {
	exp := time.Now().Add(sessionTTL).Unix()
	payload := strconv.FormatInt(exp, 10)
	token := payload + "." + a.sign(payload)

	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: token,
		Path:  "/",
		// HttpOnly: no reason for page scripts to read this, and it keeps
		// the token out of reach of any XSS.
		HttpOnly: true,
		// Lax still sends the cookie on a normal top-level navigation (so
		// a bookmarked /daftar works) while withholding it from
		// cross-site form posts.
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
		Expires:  time.Unix(exp, 0),
		MaxAge:   int(sessionTTL / time.Second),
	})
}

// clear expires the session cookie. The attributes have to match the ones
// used when issuing it, or the browser keeps the original.
func (a *auth) clear(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

// valid reports whether the request carries a session this server signed
// and that hasn't expired yet.
func (a *auth) valid(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	payload, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	// Signature first: an expiry read out of an unverified cookie is just
	// attacker-supplied text.
	if subtle.ConstantTimeCompare([]byte(sig), []byte(a.sign(payload))) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

func (a *auth) sign(payload string) string {
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// isHTTPS reports whether the browser's connection is TLS, including the
// common case of a reverse proxy terminating TLS in front of us. Setting
// Secure unconditionally would break the plain-HTTP deployment this runs
// in today; never setting it would drop protection once TLS is added.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// publicPaths are reachable without a session: the login page and its form
// target, plus the health check (monitoring shouldn't need credentials,
// and it reveals only whether ClickHouse answers).
var publicPaths = map[string]bool{
	"/login":       true,
	"/api/login":   true,
	"/api/logout":  true,
	"/healthz":     true,
	"/favicon.ico": true,
}

// requireAuth gates everything except publicPaths. API requests get a bare
// 401 so the frontend's fetch wrapper can react; anything else is a page
// navigation, so it gets redirected to the login form with a "next" so the
// user lands where they were headed.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPaths[r.URL.Path] || s.auth.valid(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		http.Redirect(w, r, "/login?next="+urlQueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	})
}

// handleLoginPage serves the login form. An already-signed-in visitor is
// bounced straight to the app rather than shown a form they don't need.
func (s *Server) handleLoginPage(staticFS http.FileSystem) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth.valid(r) {
			http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
			return
		}
		f, err := staticFS.Open("login.html")
		if err != nil {
			s.log.Error("open login.html failed", "err", err)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// A cached login page would be served to the next visitor from
		// disk, including after a logout.
		w.Header().Set("Cache-Control", "no-store")
		if _, err := io.Copy(w, f); err != nil {
			s.log.Error("write login.html failed", "err", err)
		}
	}
}

// handleLogin checks a submitted credential pair. Failures are logged with
// the submitted username but never the password, and the response says
// only that the pair was wrong — not which half.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form")
		return
	}
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")

	if !s.auth.check(username, password) {
		s.log.Warn("login failed", "username", username, "remote", clientIP(r))
		writeError(w, http.StatusUnauthorized, "Username atau password salah.")
		return
	}

	s.auth.issue(w, r)
	s.log.Info("login ok", "username", username, "remote", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"redirect": safeNext(r.PostFormValue("next"))})
}

// handleLogout drops the session and sends the browser back to the form.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.clear(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext keeps post-login redirects on this site. Anything that isn't a
// single-slash-prefixed path — an absolute URL, or the "//evil.test" form
// browsers read as protocol-relative — falls back to the home page, so the
// login form can't be used to bounce someone off-site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	if next == "/login" || strings.HasPrefix(next, "/login?") {
		return "/"
	}
	return next
}

func urlQueryEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

// clientIP is best-effort, for the login log line only. X-Forwarded-For is
// trusted here because the only thing it affects is a log field.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}
