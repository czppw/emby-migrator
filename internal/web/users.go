package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	usersFileName             = "users.json"
	passwordHashPrefix        = "$2"
	legacyPasswordHashPrefix  = "sha256:"
	legacyPasswordHashVersion = 1
	defaultUserSessionVersion = 1
)

type userRole string

const (
	roleViewer   userRole = "viewer"
	roleOperator userRole = "operator"
	roleAdmin    userRole = "admin"
)

var roleRanks = map[userRole]int{
	roleViewer:   1,
	roleOperator: 2,
	roleAdmin:    3,
}

type usersConfig struct {
	SchemaVersion int          `json:"schemaVersion,omitempty"`
	Users         []configUser `json:"users"`
}

type configUser struct {
	Username       string `json:"username"`
	Password       string `json:"password,omitempty"`
	PasswordHash   string `json:"passwordHash,omitempty"`
	Role           string `json:"role"`
	SessionVersion int    `json:"sessionVersion,omitempty"`
}

type cachedUsersConfig struct {
	path    string
	modTime int64
	size    int64
	cfg     usersConfig
}

var usersConfigMu sync.Mutex

func (s *Server) usersConfigExists() bool {
	_, err := os.Stat(s.usersPath())
	return err == nil
}

func (s *Server) usersPath() string {
	return filepath.Join(s.configDir(), usersFileName)
}

func (s *Server) authenticateLogin(username, password string) (authPrincipal, bool, error) {
	if password == "" {
		return authPrincipal{}, false, nil
	}
	users, err := s.loadUsersConfig()
	if err != nil {
		return authPrincipal{}, false, err
	}
	if len(users.Users) > 0 {
		return s.authenticateConfiguredUser(users, username, password)
	}
	if !sameLoginName(username, "admin") {
		return authPrincipal{}, false, nil
	}
	if !constantTimeStringEqual(password, s.cfg.AdminPassword) {
		return authPrincipal{}, false, nil
	}
	return defaultPrincipal(), true, nil
}

func (s *Server) authenticateConfiguredUser(cfg usersConfig, username, password string) (authPrincipal, bool, error) {
	username = strings.TrimSpace(username)
	for _, user := range cfg.Users {
		if !sameLoginName(username, user.Username) {
			continue
		}
		role, ok := normalizeRole(user.Role)
		if !ok {
			return authPrincipal{}, false, nil
		}
		if user.verifyPassword(password) {
			if passwordHashNeedsUpgrade(user.PasswordHash) {
				if err := s.upgradePasswordHash(user.Username, password); err != nil {
					return authPrincipal{}, false, err
				}
			}
			return authPrincipal{Username: strings.TrimSpace(user.Username), Role: string(role), SessionVersion: normalizedSessionVersion(user.SessionVersion)}, true, nil
		}
		return authPrincipal{}, false, nil
	}
	// Equalize timing with the known-username path so response latency cannot
	// be used to enumerate valid usernames.
	dummyPasswordCompare(password)
	return authPrincipal{}, false, nil
}

// dummyBcryptHash is a throwaway hash used only to burn a bcrypt comparison
// for unknown usernames.
var dummyBcryptHash, _ = newPasswordHash("emby-migrator-timing-equalizer-3f9c2a")

func dummyPasswordCompare(password string) {
	_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
}

func (s *Server) changeCurrentPassword(username, oldPassword, newPassword string) error {
	if strings.TrimSpace(newPassword) == "" {
		return fmt.Errorf("newPassword is required")
	}
	_, err := s.changeCurrentAccount(username, oldPassword, username, newPassword)
	return err
}

func (s *Server) changeCurrentUsername(username, currentPassword, newUsername string) (string, error) {
	return s.changeCurrentAccount(username, currentPassword, newUsername, "")
}

func (s *Server) changeCurrentAccount(username, currentPassword, newUsername, newPassword string) (string, error) {
	s.usersWriteMu.Lock()
	defer s.usersWriteMu.Unlock()
	newUsername = strings.TrimSpace(newUsername)
	if err := validateUsername(newUsername); err != nil {
		return "", err
	}
	usernameChanged := !sameLoginName(username, newUsername)
	passwordChanged := newPassword != ""
	if !usernameChanged && !passwordChanged {
		return "", fmt.Errorf("no account changes requested")
	}
	cfg, err := s.loadUsersConfig()
	if err != nil {
		return "", err
	}
	cfg.Users = append([]configUser(nil), cfg.Users...)
	if len(cfg.Users) == 0 {
		if !sameLoginName(username, "admin") || !constantTimeStringEqual(currentPassword, s.cfg.AdminPassword) {
			return "", fmt.Errorf("invalid current password")
		}
		passwordToStore := currentPassword
		if passwordChanged {
			passwordToStore = newPassword
		}
		hash, err := newPasswordHash(passwordToStore)
		if err != nil {
			return "", err
		}
		cfg = usersConfig{
			SchemaVersion: 1,
			Users: []configUser{{
				Username:       newUsername,
				PasswordHash:   hash,
				Role:           string(roleAdmin),
				SessionVersion: defaultUserSessionVersion + 1,
			}},
		}
		if err := writeUsersConfig(s.usersPath(), cfg); err != nil {
			return "", err
		}
		s.clearUsersCache()
		return newUsername, nil
	}

	currentIndex := -1
	for i := range cfg.Users {
		if sameLoginName(username, cfg.Users[i].Username) {
			currentIndex = i
			continue
		}
		if sameLoginName(newUsername, cfg.Users[i].Username) {
			return "", fmt.Errorf("username is already in use")
		}
	}
	if currentIndex < 0 {
		return "", fmt.Errorf("current user not found")
	}
	if !cfg.Users[currentIndex].verifyPassword(currentPassword) {
		return "", fmt.Errorf("invalid current password")
	}
	cfg.Users[currentIndex].Username = newUsername
	cfg.Users[currentIndex].SessionVersion = normalizedSessionVersion(cfg.Users[currentIndex].SessionVersion) + 1
	if passwordChanged {
		hash, err := newPasswordHash(newPassword)
		if err != nil {
			return "", err
		}
		cfg.Users[currentIndex].Password = ""
		cfg.Users[currentIndex].PasswordHash = hash
	}
	if err := writeUsersConfig(s.usersPath(), cfg); err != nil {
		return "", err
	}
	s.clearUsersCache()
	return newUsername, nil
}

func validateUsername(username string) error {
	username = strings.TrimSpace(username)
	length := utf8.RuneCountInString(username)
	if length < 3 || length > 32 {
		return fmt.Errorf("username must contain 3 to 32 characters")
	}
	for index, char := range []rune(username) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			continue
		}
		if index > 0 && (char == '.' || char == '-' || char == '_') {
			continue
		}
		return fmt.Errorf("username contains unsupported characters")
	}
	return nil
}

func (s *Server) principalActive(principal authPrincipal) bool {
	cfg, err := s.loadUsersConfig()
	if err != nil {
		return false
	}
	if len(cfg.Users) == 0 {
		return sameLoginName(principal.Username, "admin") && principal.Role == string(roleAdmin) && principal.SessionVersion == defaultUserSessionVersion
	}
	for _, user := range cfg.Users {
		if !sameLoginName(principal.Username, user.Username) {
			continue
		}
		role, ok := normalizeRole(user.Role)
		return ok && principal.Role == string(role) && principal.SessionVersion == normalizedSessionVersion(user.SessionVersion)
	}
	return false
}

func sameLoginName(requested, configured string) bool {
	requested = strings.TrimSpace(requested)
	configured = strings.TrimSpace(configured)
	if requested == "" {
		requested = "admin"
	}
	return strings.EqualFold(requested, configured)
}

func (u configUser) verifyPassword(password string) bool {
	if strings.TrimSpace(u.PasswordHash) != "" {
		return verifyPasswordHash(password, u.PasswordHash)
	}
	if u.Password != "" {
		return constantTimeStringEqual(password, u.Password)
	}
	return false
}

func verifyPasswordHash(password, encoded string) bool {
	encoded = strings.TrimSpace(encoded)
	if strings.HasPrefix(encoded, passwordHashPrefix) {
		return bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password)) == nil
	}
	parts := strings.Split(encoded, ":")
	if len(parts) != 4 || parts[0]+":" != legacyPasswordHashPrefix || parts[1] != fmt.Sprintf("v%d", legacyPasswordHashVersion) {
		return false
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := saltedPasswordSum([]byte(password), salt)
	return subtleHashEqual(got[:], want)
}

func subtleHashEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func newPasswordHash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

func passwordHashNeedsUpgrade(encoded string) bool {
	encoded = strings.TrimSpace(encoded)
	return encoded != "" && !strings.HasPrefix(encoded, passwordHashPrefix)
}

func normalizedSessionVersion(version int) int {
	if version <= 0 {
		return defaultUserSessionVersion
	}
	return version
}

func (s *Server) upgradePasswordHash(username, password string) error {
	s.usersWriteMu.Lock()
	defer s.usersWriteMu.Unlock()
	cfg, err := s.loadUsersConfig()
	if err != nil {
		return err
	}
	cfg.Users = append([]configUser(nil), cfg.Users...)
	for i := range cfg.Users {
		user := &cfg.Users[i]
		if !sameLoginName(username, user.Username) {
			continue
		}
		if !passwordHashNeedsUpgrade(user.PasswordHash) || !user.verifyPassword(password) {
			return nil
		}
		hash, err := newPasswordHash(password)
		if err != nil {
			return err
		}
		user.Password = ""
		user.PasswordHash = hash
		if err := writeUsersConfig(s.usersPath(), cfg); err != nil {
			return err
		}
		s.clearUsersCache()
		return nil
	}
	return nil
}

func saltedPasswordSum(password, salt []byte) [32]byte {
	data := make([]byte, 0, len(salt)+len(password))
	data = append(data, salt...)
	data = append(data, password...)
	return sha256.Sum256(data)
}

func normalizeRole(value string) (userRole, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(roleAdmin):
		return roleAdmin, true
	case string(roleOperator):
		return roleOperator, true
	case "", string(roleViewer):
		return roleViewer, true
	default:
		return "", false
	}
}

func roleAllows(actual, required userRole) bool {
	return roleRanks[actual] >= roleRanks[required]
}

func (s *Server) loadUsersConfig() (usersConfig, error) {
	path := s.usersPath()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return usersConfig{}, nil
		}
		return usersConfig{}, fmt.Errorf("read users config metadata: %w", err)
	}
	usersConfigMu.Lock()
	defer usersConfigMu.Unlock()
	if s.usersCache.path == path && s.usersCache.modTime == info.ModTime().UnixNano() && s.usersCache.size == info.Size() {
		return s.usersCache.cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return usersConfig{}, fmt.Errorf("read users config: %w", err)
	}
	var cfg usersConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return usersConfig{}, fmt.Errorf("parse users config: %w", err)
	}
	cfg, changed, err := normalizeUsersConfig(cfg)
	if err != nil {
		return usersConfig{}, err
	}
	if changed {
		if err := writeUsersConfig(path, cfg); err != nil {
			return usersConfig{}, err
		}
		if refreshed, statErr := os.Stat(path); statErr == nil {
			info = refreshed
		}
	}
	s.usersCache = cachedUsersConfig{
		path:    path,
		modTime: info.ModTime().UnixNano(),
		size:    info.Size(),
		cfg:     cfg,
	}
	return cfg, nil
}

func normalizeUsersConfig(cfg usersConfig) (usersConfig, bool, error) {
	changed := false
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = 1
		changed = true
	}
	out := make([]configUser, 0, len(cfg.Users))
	seen := map[string]bool{}
	for _, user := range cfg.Users {
		user.Username = strings.TrimSpace(user.Username)
		if user.Username == "" {
			return usersConfig{}, false, fmt.Errorf("users config has a blank username")
		}
		key := strings.ToLower(user.Username)
		if seen[key] {
			return usersConfig{}, false, fmt.Errorf("users config has duplicate username %q", user.Username)
		}
		seen[key] = true
		role, ok := normalizeRole(user.Role)
		if !ok {
			return usersConfig{}, false, fmt.Errorf("users config has invalid role for %q", user.Username)
		}
		if user.Role != string(role) {
			user.Role = string(role)
			changed = true
		}
		if user.Password != "" {
			hash, err := newPasswordHash(user.Password)
			if err != nil {
				return usersConfig{}, false, err
			}
			user.PasswordHash = hash
			user.Password = ""
			changed = true
		}
		if strings.TrimSpace(user.PasswordHash) == "" {
			return usersConfig{}, false, fmt.Errorf("users config entry %q is missing passwordHash", user.Username)
		}
		if user.SessionVersion <= 0 {
			user.SessionVersion = defaultUserSessionVersion
			changed = true
		}
		out = append(out, user)
	}
	cfg.Users = out
	return cfg, changed, nil
}

func writeUsersConfig(path string, cfg usersConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode users config: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(path, usersFileName+".tmp-", data, 0o600); err != nil {
		return fmt.Errorf("save users config: %w", err)
	}
	return nil
}

func (s *Server) clearUsersCache() {
	usersConfigMu.Lock()
	s.usersCache = cachedUsersConfig{}
	usersConfigMu.Unlock()
}

func (s *Server) handleUsersNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, fmt.Errorf("not found"))
}
