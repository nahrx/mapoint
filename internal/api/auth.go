package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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

// Account is one login the dashboard accepts.
type Account struct {
	Username string
	Password string
}

// auth holds every accepted account, each with its own cookie-signing key.
type auth struct {
	accounts []account
}

type account struct {
	username string
	password string
	key      []byte
}

// newAuth derives each account's cookie-signing key from that account's own
// credentials. Two properties fall out of it for free:
//
//   - Changing one account's password invalidates that account's sessions
//     and nobody else's. A single shared key would log everyone out every
//     time any password changed, or any account was added.
//   - Removing an account from .env kills its sessions immediately, because
//     valid() can no longer find a key to verify its token with.
//
// No session store and no extra secret to rotate, and it gives away
// nothing: whoever knows a password can log in anyway, so being able to
// derive that account's key from it is not a further loss. A random key
// generated at startup would instead log everyone out on every deploy.
func newAuth(accounts []Account) *auth {
	a := &auth{accounts: make([]account, 0, len(accounts))}
	for _, acc := range accounts {
		sum := sha256.Sum256([]byte("se2026-titik-maps/session/v1\x00" + acc.Username + "\x00" + acc.Password))
		a.accounts = append(a.accounts, account{username: acc.Username, password: acc.Password, key: sum[:]})
	}
	return a
}

// check verifies a submitted pair and returns the matched username.
//
// The loop deliberately runs to the end instead of returning on the first
// match: an early return would make a request that matches the first
// configured account measurably faster than one matching the last, which
// leaks which account a guess hit. Both comparisons are constant-time for
// the same reason, so a wrong username is indistinguishable from a wrong
// password.
func (a *auth) check(username, password string) (string, bool) {
	matched := ""
	ok := 0
	for _, acc := range a.accounts {
		u := subtle.ConstantTimeCompare([]byte(username), []byte(acc.username))
		p := subtle.ConstantTimeCompare([]byte(password), []byte(acc.password))
		if u&p == 1 {
			matched = acc.username
			ok = 1
		}
	}
	return matched, ok == 1
}

// lookup finds the account a session token claims to belong to. Returns nil
// when that account no longer exists, which is what makes deleting an
// account take effect immediately.
func (a *auth) lookup(username string) *account {
	for i := range a.accounts {
		if a.accounts[i].username == username {
			return &a.accounts[i]
		}
	}
	return nil
}

// issue writes a fresh session cookie for one account. The token is
// "<expiry unix>.<base64url username>.<hmac>" — stateless, so nothing has
// to be remembered across restarts, and unforgeable without that account's
// key. The username is base64url-encoded rather than raw so it can't
// contain the "." the token splits on, and so an address like
// viewer6400@bps.go.id stays inside the character set cookies allow.
func (a *auth) issue(w http.ResponseWriter, r *http.Request, username string) {
	acc := a.lookup(username)
	if acc == nil {
		return
	}
	exp := time.Now().Add(sessionTTL).Unix()
	payload := strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString([]byte(username))
	token := payload + "." + acc.sign(payload)

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
// for a still-existing account, and that hasn't expired yet. The username
// it returns is empty unless the session is valid.
func (a *auth) valid(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	expStr, rest, ok := strings.Cut(c.Value, ".")
	if !ok {
		return "", false
	}
	userB64, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(userB64)
	if err != nil {
		return "", false
	}
	// The account is looked up before the signature is checked only to
	// find which key to verify with — nothing is trusted until the HMAC
	// below passes. An account deleted from .env has no key here, so its
	// outstanding sessions stop working at once.
	acc := a.lookup(string(raw))
	if acc == nil {
		return "", false
	}
	payload := expStr + "." + userB64
	if subtle.ConstantTimeCompare([]byte(sig), []byte(acc.sign(payload))) != 1 {
		return "", false
	}
	// Only now is the expiry worth reading: before the HMAC passed it was
	// just attacker-supplied text.
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", false
	}
	if time.Now().Unix() >= exp {
		return "", false
	}
	return acc.username, true
}

func (a *account) sign(payload string) string {
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
		if publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := s.auth.valid(r); ok {
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
		if _, ok := s.auth.valid(r); ok {
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

	matched, ok := s.auth.check(username, password)
	if !ok {
		s.log.Warn("login failed", "username", username, "remote", clientIP(r))
		writeError(w, http.StatusUnauthorized, "Username atau password salah.")
		return
	}

	s.auth.issue(w, r, matched)
	s.log.Info("login ok", "username", matched, "remote", clientIP(r))
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
