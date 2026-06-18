package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookieName = "emby_migrator_session"

type authContextKey struct{}

type authPrincipal struct {
	Username string `json:"user"`
	Role     string `json:"role"`
}

type authStatusResponse struct {
	Enabled       bool   `json:"enabled"`
	Authenticated bool   `json:"authenticated"`
	ToolVersion   string `json:"toolVersion"`
	User          string `json:"user,omitempty"`
	Role          string `json:"role,omitempty"`
	Warning       string `json:"warning,omitempty"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordChangeRequest struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

type passwordChangeResponse struct {
	OK bool `json:"ok"`
}

type sessionPayload struct {
	Expires  int64  `json:"exp"`
	Nonce    string `json:"nonce"`
	Username string `json:"user"`
	Role     string `json:"role"`
}

func defaultPrincipal() authPrincipal {
	return authPrincipal{Username: "admin", Role: string(roleAdmin)}
}

func (s *Server) authEnabled() bool {
	return strings.TrimSpace(s.cfg.AdminPassword) != "" || s.usersConfigExists()
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
		principal := defaultPrincipal()
		writeJSON(w, http.StatusOK, authStatusResponse{
			Enabled:       false,
			Authenticated: true,
			ToolVersion:   s.cfg.Version,
			User:          principal.Username,
			Role:          principal.Role,
			Warning:       "EMBY_MIGRATOR_PASSWORD is empty; web login protection is disabled.",
		})
		return
	}
	principal, authenticated := s.sessionPrincipal(r)
	response := authStatusResponse{
		Enabled:       true,
		Authenticated: authenticated,
		ToolVersion:   s.cfg.Version,
	}
	if authenticated {
		response.User = principal.Username
		response.Role = principal.Role
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		principal := defaultPrincipal()
		writeJSON(w, http.StatusOK, authStatusResponse{
			Enabled:       false,
			Authenticated: true,
			ToolVersion:   s.cfg.Version,
			User:          principal.Username,
			Role:          principal.Role,
		})
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	principal, ok, err := s.authenticateLogin(req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("invalid username or password"))
		return
	}
	http.SetCookie(w, s.sessionCookie(r, s.newSessionToken(principal)))
	writeJSON(w, http.StatusOK, authStatusResponse{
		Enabled:       true,
		Authenticated: true,
		ToolVersion:   s.cfg.Version,
		User:          principal.Username,
		Role:          principal.Role,
	})
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

func (s *Server) handleAuthPasswordChange(w http.ResponseWriter, r *http.Request) {
	principal, ok := currentPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("not logged in"))
		return
	}
	var req passwordChangeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.NewPassword) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("newPassword is required"))
		return
	}
	if err := s.changeCurrentPassword(principal.Username, req.OldPassword, req.NewPassword); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "invalid current password") {
			status = http.StatusUnauthorized
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, passwordChangeResponse{OK: true})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return s.requireRole(roleViewer, next)
}

func (s *Server) requireRole(minRole userRole, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() {
			principal := defaultPrincipal()
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, principal)))
			return
		}
		principal, ok := s.sessionPrincipal(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, fmt.Errorf("not logged in"))
			return
		}
		role, roleOK := normalizeRole(principal.Role)
		if !roleOK || !roleAllows(role, minRole) {
			writeError(w, http.StatusForbidden, fmt.Errorf("insufficient role"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, principal)))
	})
}

func currentPrincipal(r *http.Request) (authPrincipal, bool) {
	principal, ok := r.Context().Value(authContextKey{}).(authPrincipal)
	return principal, ok
}

func (s *Server) newSessionToken(principal authPrincipal) string {
	if strings.TrimSpace(principal.Username) == "" || strings.TrimSpace(principal.Role) == "" {
		principal = defaultPrincipal()
	}
	expires := time.Now().Add(12 * time.Hour).Unix()
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		copy(nonce, []byte(time.Now().Format(time.RFC3339Nano)))
	}
	payload := sessionPayload{
		Expires:  expires,
		Nonce:    hex.EncodeToString(nonce),
		Username: principal.Username,
		Role:     principal.Role,
	}
	payloadBytes, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write(payloadBytes)
	signature := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func (s *Server) validSession(r *http.Request) bool {
	_, ok := s.sessionPrincipal(r)
	return ok
}

func (s *Server) sessionPrincipal(r *http.Request) (authPrincipal, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return authPrincipal{}, false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return authPrincipal{}, false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return authPrincipal{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return authPrincipal{}, false
	}
	mac := hmac.New(sha256.New, s.sessionSecret)
	mac.Write(payloadBytes)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return authPrincipal{}, false
	}
	if principal, ok := sessionPayloadPrincipal(payloadBytes); ok {
		return principal, true
	}
	return s.legacySessionPrincipal(payloadBytes)
}

func sessionPayloadPrincipal(payloadBytes []byte) (authPrincipal, bool) {
	var payload sessionPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return authPrincipal{}, false
	}
	if payload.Expires == 0 || time.Now().Unix() >= payload.Expires {
		return authPrincipal{}, false
	}
	role, ok := normalizeRole(payload.Role)
	if !ok || strings.TrimSpace(payload.Username) == "" {
		return authPrincipal{}, false
	}
	return authPrincipal{Username: strings.TrimSpace(payload.Username), Role: string(role)}, true
}

func (s *Server) legacySessionPrincipal(payloadBytes []byte) (authPrincipal, bool) {
	if s.usersConfigExists() {
		return authPrincipal{}, false
	}
	payload := string(payloadBytes)
	expiresText, _, ok := strings.Cut(payload, ":")
	if !ok {
		return authPrincipal{}, false
	}
	expires, err := strconv.ParseInt(expiresText, 10, 64)
	if err != nil || time.Now().Unix() >= expires {
		return authPrincipal{}, false
	}
	return defaultPrincipal(), true
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

func constantTimeStringEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(hashForCompare([]byte(a))), []byte(hashForCompare([]byte(b)))) == 1
}
