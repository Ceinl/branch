package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	shooBaseURL     = "https://shoo.dev"
	shooIssuer      = "https://shoo.dev"
	sessionCookie   = "branch_session"
	jwksCacheMaxAge = time.Hour
)

type authUser struct {
	ID      string    `json:"id"`
	Email   string    `json:"email,omitempty"`
	Name    string    `json:"name,omitempty"`
	Picture string    `json:"picture,omitempty"`
	Expires time.Time `json:"expires"`
}

type authSession struct {
	User authUser  `json:"user"`
	Exp  time.Time `json:"exp"`
}

// sessionStore keeps sessions keyed by the SHA-256 of the cookie token, so
// the optional persistence file never contains usable tokens. With an empty
// path the store is memory-only.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]authSession
	path     string
}

func newSessionStore(path string) *sessionStore {
	s := &sessionStore{sessions: make(map[string]authSession), path: path}
	s.load()
	return s
}

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *sessionStore) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	stored := map[string]authSession{}
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	now := time.Now()
	for key, session := range stored {
		if now.Before(session.Exp) {
			s.sessions[key] = session
		}
	}
}

func (s *sessionStore) saveLocked() {
	if s.path == "" {
		return
	}
	data, err := json.Marshal(s.sessions)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return
	}
	_ = os.WriteFile(s.path, data, 0600)
}

func (s *sessionStore) create(user authUser, exp time.Time) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[hashSessionToken(token)] = authSession{User: user, Exp: exp}
	s.saveLocked()
	return token, nil
}

func (s *sessionStore) get(token string) (authUser, bool) {
	if token == "" {
		return authUser{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := hashSessionToken(token)
	session, ok := s.sessions[key]
	if !ok {
		return authUser{}, false
	}
	if time.Now().After(session.Exp) {
		delete(s.sessions, key)
		s.saveLocked()
		return authUser{}, false
	}
	return session.User, true
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, hashSessionToken(token))
	s.saveLocked()
}

// loginLimiter caps sign-in attempts per client IP to slow down brute force
// against the session endpoint.
type loginLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{hits: map[string][]time.Time{}}
}

func (l *loginLimiter) allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-window)
	kept := l.hits[key][:0]
	for _, hit := range l.hits[key] {
		if hit.After(cutoff) {
			kept = append(kept, hit)
		}
	}
	if len(kept) >= limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, time.Now())
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type shooVerifier struct {
	mu        sync.Mutex
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time
	client    *http.Client
}

func newShooVerifier() *shooVerifier {
	return &shooVerifier{
		keys:   make(map[string]*ecdsa.PublicKey),
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *shooVerifier) verify(idToken string, appOrigin string) (authUser, time.Time, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return authUser{}, time.Time{}, errors.New("invalid Shoo token format")
	}

	headerBytes, err := decodeBase64URL(parts[0])
	if err != nil {
		return authUser{}, time.Time{}, errors.New("invalid Shoo token header")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return authUser{}, time.Time{}, errors.New("invalid Shoo token header")
	}
	if header.Alg != "ES256" || header.Kid == "" {
		return authUser{}, time.Time{}, errors.New("unsupported Shoo token header")
	}

	signature, err := decodeBase64URL(parts[2])
	if err != nil || len(signature) != 64 {
		return authUser{}, time.Time{}, errors.New("invalid Shoo token signature")
	}
	key, err := v.key(header.Kid)
	if err != nil {
		return authUser{}, time.Time{}, err
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(key, hash[:], r, s) {
		return authUser{}, time.Time{}, errors.New("invalid Shoo token signature")
	}

	payloadBytes, err := decodeBase64URL(parts[1])
	if err != nil {
		return authUser{}, time.Time{}, errors.New("invalid Shoo token payload")
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return authUser{}, time.Time{}, errors.New("invalid Shoo token payload")
	}

	if stringClaim(claims, "iss") != shooIssuer {
		return authUser{}, time.Time{}, errors.New("invalid Shoo issuer")
	}
	expectedAudience := "origin:" + strings.TrimRight(appOrigin, "/")
	if !audienceMatches(claims["aud"], expectedAudience) {
		return authUser{}, time.Time{}, fmt.Errorf("invalid Shoo audience, expected %s", expectedAudience)
	}
	expUnix, ok := numberClaim(claims, "exp")
	if !ok {
		return authUser{}, time.Time{}, errors.New("Shoo token missing exp")
	}
	exp := time.Unix(expUnix, 0)
	if !time.Now().Before(exp) {
		return authUser{}, time.Time{}, errors.New("Shoo token expired")
	}
	userID := stringClaim(claims, "pairwise_sub")
	if userID == "" {
		userID = stringClaim(claims, "sub")
	}
	if userID == "" {
		return authUser{}, time.Time{}, errors.New("Shoo token missing pairwise_sub")
	}

	return authUser{
		ID:      userID,
		Email:   stringClaim(claims, "email"),
		Name:    stringClaim(claims, "name"),
		Picture: stringClaim(claims, "picture"),
		Expires: exp,
	}, exp, nil
}

func (v *shooVerifier) key(kid string) (*ecdsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if key, ok := v.keys[kid]; ok && time.Since(v.fetchedAt) < jwksCacheMaxAge {
		return key, nil
	}
	if err := v.fetchKeysLocked(); err != nil {
		return nil, err
	}
	key, ok := v.keys[kid]
	if !ok {
		return nil, errors.New("Shoo signing key not found")
	}
	return key, nil
}

func (v *shooVerifier) fetchKeysLocked() error {
	req, err := http.NewRequest(http.MethodGet, shooBaseURL+"/.well-known/jwks.json", nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch Shoo JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch Shoo JWKS: %s", resp.Status)
	}
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Kid string `json:"kid"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("decode Shoo JWKS: %w", err)
	}
	keys := make(map[string]*ecdsa.PublicKey)
	for _, jwk := range jwks.Keys {
		if jwk.Kty != "EC" || jwk.Crv != "P-256" || jwk.Kid == "" {
			continue
		}
		xBytes, err := decodeBase64URL(jwk.X)
		if err != nil {
			continue
		}
		yBytes, err := decodeBase64URL(jwk.Y)
		if err != nil {
			continue
		}
		keys[jwk.Kid] = &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(xBytes),
			Y:     new(big.Int).SetBytes(yBytes),
		}
	}
	if len(keys) == 0 {
		return errors.New("Shoo JWKS contained no usable keys")
	}
	v.keys = keys
	v.fetchedAt = time.Now()
	return nil
}

func (a *app) handleSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if a.blockCrossOrigin(w, r) {
		return
	}
	if !a.auth {
		writeJSON(w, http.StatusOK, map[string]any{"user": localUser()})
		return
	}
	switch r.Method {
	case http.MethodGet:
		user, err := a.requireUser(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"user": user})
	case http.MethodPost:
		if !a.logins.allow(clientIP(r), 10, time.Minute) {
			writeError(w, http.StatusTooManyRequests, "too many sign-in attempts, try again in a minute")
			return
		}
		var req struct {
			IDToken string `json:"idToken"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IDToken == "" {
			writeError(w, http.StatusBadRequest, "missing idToken")
			return
		}
		origin := a.originForRequest(r)
		user, exp, err := a.shoo.verify(req.IDToken, origin)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if !a.emailAllowed(user.Email) {
			writeError(w, http.StatusForbidden, "this account is not allowed on this server")
			return
		}
		sessionToken, err := a.sessions.create(user, exp)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    sessionToken,
			Path:     "/",
			Expires:  exp,
			SameSite: http.SameSiteLaxMode,
			HttpOnly: true,
			Secure:   strings.HasPrefix(origin, "https://"),
		})
		writeJSON(w, http.StatusOK, map[string]any{"user": user})
	case http.MethodDelete:
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			a.sessions.delete(cookie.Value)
		}
		a.clearSessionCookie(w, r)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *app) requireUser(r *http.Request) (authUser, error) {
	if !a.auth {
		return localUser(), nil
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if user, ok := a.sessions.get(cookie.Value); ok {
			return user, nil
		}
	}
	return authUser{}, errors.New("login required")
}

func localUser() authUser {
	return authUser{ID: "local", Name: "Local user", Expires: time.Now().Add(24 * time.Hour)}
}

func (a *app) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   strings.HasPrefix(a.originForRequest(r), "https://"),
	})
}

func (a *app) originForRequest(r *http.Request) string {
	if a.appOrigin != "" {
		return a.appOrigin
	}
	return a.rawOriginForRequest(r)
}

func (a *app) shooOriginForRequest(r *http.Request) string {
	if a.appOrigin != "" {
		return a.appOrigin
	}
	origin := a.rawOriginForRequest(r)
	if !strings.HasPrefix(origin, "http://") {
		return origin
	}
	host := strings.TrimPrefix(origin, "http://")
	if strings.HasPrefix(host, "0.0.0.0:") || strings.HasPrefix(host, "127.0.0.1:") || strings.HasPrefix(host, "[::1]:") {
		if i := strings.LastIndex(host, ":"); i >= 0 {
			return "http://localhost" + host[i:]
		}
		return "http://localhost"
	}
	if host == "0.0.0.0" || host == "127.0.0.1" || host == "[::1]" {
		return "http://localhost"
	}
	return origin
}

func (a *app) rawOriginForRequest(r *http.Request) string {
	scheme := firstHeader(r.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := firstHeader(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

func firstHeader(value string) string {
	if i := strings.Index(value, ","); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

func decodeBase64URL(value string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

func stringClaim(claims map[string]any, name string) string {
	value, _ := claims[name].(string)
	return value
}

func numberClaim(claims map[string]any, name string) (int64, bool) {
	switch value := claims[name].(type) {
	case float64:
		return int64(value), true
	case int64:
		return value, true
	case int:
		return int64(value), true
	default:
		return 0, false
	}
}

func audienceMatches(value any, expected string) bool {
	switch aud := value.(type) {
	case string:
		return aud == expected
	case []any:
		for _, item := range aud {
			if s, ok := item.(string); ok && s == expected {
				return true
			}
		}
	}
	return false
}

type userContextKey struct{}

func withUser(ctx context.Context, user authUser) context.Context {
	return context.WithValue(ctx, userContextKey{}, user)
}

func userFromRequest(r *http.Request) authUser {
	user, _ := r.Context().Value(userContextKey{}).(authUser)
	return user
}
