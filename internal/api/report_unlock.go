package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The second password in front of the kabupaten/kota-wide download.
//
// Every dashboard account may download a kecamatan; a whole kabupaten/kota
// — thousands of files, up to ~550k rows, about a minute of server time —
// is gated behind REPORT_KABKOTA_PASSWORD on top of the login. The password
// itself never travels in a download URL (those end up in browser history
// and the request log): the dialog first POSTs it to /api/report-unlock,
// gets back a short-lived token, and puts that token on the GET. The
// token is an HMAC over its own expiry, keyed from the password, so it
// needs no server-side state and survives a restart like the session
// cookies do (see newAuth for the same reasoning).

const (
	// unlockParam carries the token on /api/list/pdf and /api/list/xlsx.
	unlockParam = "unlock"
	// unlockTTL is how long a token is good for. The dialog uses it within
	// a second; the slack is for a slow network, not for reuse.
	unlockTTL = 10 * time.Minute
	// unlockFailDelay slows a wrong-password loop without any bookkeeping.
	// One shared password per deployment is exactly the kind a script
	// would try to guess.
	unlockFailDelay = time.Second
)

// reportUnlock checks passwords and mints/verifies tokens. A zero password
// disables it: check and valid both fail, so an unset REPORT_KABKOTA_PASSWORD
// closes the width rather than opening it.
type reportUnlock struct {
	password string
	key      []byte
}

func newReportUnlock(password string) *reportUnlock {
	if password == "" {
		return &reportUnlock{}
	}
	sum := sha256.Sum256([]byte("se2026-report-unlock:" + password))
	return &reportUnlock{password: password, key: sum[:]}
}

func (u *reportUnlock) enabled() bool { return u.password != "" }

// check reports whether candidate is the password, in constant time.
func (u *reportUnlock) check(candidate string) bool {
	return u.enabled() && subtle.ConstantTimeCompare([]byte(candidate), []byte(u.password)) == 1
}

// token mints "<expiry unix>.<hex hmac>".
func (u *reportUnlock) token(now time.Time) string {
	exp := strconv.FormatInt(now.Add(unlockTTL).Unix(), 10)
	return exp + "." + u.sign(exp)
}

func (u *reportUnlock) sign(exp string) string {
	m := hmac.New(sha256.New, u.key)
	m.Write([]byte(exp))
	return hex.EncodeToString(m.Sum(nil))
}

// valid reports whether tok was minted by token and has not expired.
func (u *reportUnlock) valid(tok string) bool {
	if !u.enabled() {
		return false
	}
	exp, mac, ok := strings.Cut(tok, ".")
	if !ok {
		return false
	}
	if !hmac.Equal([]byte(mac), []byte(u.sign(exp))) {
		return false
	}
	t, err := strconv.ParseInt(exp, 10, 64)
	return err == nil && time.Now().Unix() <= t
}

// handleReportUnlock exchanges the second password for a download token.
//
//	POST /api/report-unlock  {"password": "..."}
//	200 {"token": "...", "expires_in": 600}
//	403 {"error": "wrong password"}            — after a one-second pause
//	503 {"error": "kabupaten/kota-wide download is not enabled on this server"}
//
// Behind requireAuth like every /api route, so only a logged-in account
// can even try.
func (s *Server) handleReportUnlock(w http.ResponseWriter, r *http.Request) {
	if !s.unlock.enabled() {
		writeError(w, http.StatusServiceUnavailable, "kabupaten/kota-wide download is not enabled on this server (REPORT_KABKOTA_PASSWORD is empty)")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !s.unlock.check(body.Password) {
		s.log.Warn("report unlock failed", "remote", clientIP(r))
		select {
		case <-time.After(unlockFailDelay):
		case <-r.Context().Done():
			return
		}
		writeError(w, http.StatusForbidden, "wrong password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      s.unlock.token(time.Now()),
		"expires_in": int(unlockTTL.Seconds()),
	})
}
