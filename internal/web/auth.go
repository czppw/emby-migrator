package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookieName = "emby_migrator_session"

type authStatusResponse struct {
	Enabled       bool   `json:"enabled"`
	Authenticated bool   `json:"authenticated"`
	ToolVersion   string `json:"toolVersion"`
	Warning       string `json:"warning,omitempty"`
}

type loginRequest struct {
	Password string `json:"password"`
}

func (s *Server) authEnabled() bool {
	return strings.TrimSpace(s.cfg.AdminPassword) != ""
}

func makeSessionSecret(value string) []byte {
	if value != "" {
		return []byte(value)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return []byte(time.Now().Format(time.RFC3339Nano))
	}
	return buf
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		writeJSON(w, http.StatusOK, authStatusResponse{
			Enabled:       false,
			Authenticated: true,
			ToolVersion:   s.cfg.Version,
			Warning:       "未设置 EMBY_MIGRATOR_PASSWORD，当前 Web 页面未启用登录保护。",
		})
		return
	}
	writeJSON(w, http.StatusOK, authStatusResponse{
		Enabled:       true,
		Authenticated: s.validSession(r),
		ToolVersion:   s.cfg.Version,
	})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		writeJSON(w, http.StatusOK, authStatusResponse{Enabled: false, Authenticated: true, ToolVersion: s.cfg.Version})
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	expected := []byte(s.cfg.AdminPassword)
	actual := []byte(req.Password)
	if subtle.ConstantTimeCompare([]byte(hashForCompare(actual)), []byte(hashForCompare(expected))) != 1 {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("密码错误"))
		return
	}
	http.SetCookie(w, s.sessionCookie(r, s.newSessionToken()))
	writeJSON(w, http.StatusOK, authStatusResponse{Enabled: true, Authenticated: true, ToolVersion: s.cfg.Version})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() || s.validSession(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, fmt.Errorf("未登录"))
	})
}

func (s *Server) newSessionToken() string {
	expires := time.Now().Add(12 * time.Hour).Unix()
	nonce := make([]byte, 16)
	rand.Read(nonce)
	payload := strconv.FormatInt(expires, 10) + ":" + hex.EncodeToString(nonce)
	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write([]byte(payload))
	signature := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func (s *Server) validSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write(payloadBytes)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return false
	}
	payload := string(payloadBytes)
	expiresText, _, ok := strings.Cut(payload, ":")
	if !ok {
		return false
	}
	expires, err := strconv.ParseInt(expiresText, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < expires
}

func (s *Server) sessionCookie(r *http.Request, token string) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int((12 * time.Hour).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	}
}

func hashForCompare(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
