package viewerauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var issueIdentifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[1-9][0-9]*$`)

const (
	authorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenEndpoint         = "https://oauth2.googleapis.com/token"
	userinfoEndpoint      = "https://openidconnect.googleapis.com/v1/userinfo"
	stateCookieName       = "__Host-factory_oauth_state"
	sessionCookieName     = "__Host-factory_session"
	stateLifetime         = 10 * time.Minute
	sessionLifetime       = 24 * time.Hour
	maxResponseBytes      = 64 << 10
	maxCookieBytes        = 4096
)

type Config struct {
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	AllowedEmails []string
	SessionKey    []byte
	HTTPClient    *http.Client
	Now           func() time.Time
	Random        io.Reader
}

type Authenticator struct {
	clientID      string
	clientSecret  string
	redirectURL   string
	allowedEmails map[string]struct{}
	loginHint     string
	sessionKey    []byte
	httpClient    *http.Client
	now           func() time.Time
	random        io.Reader
}

type stateClaims struct {
	State     string `json:"state"`
	Next      string `json:"next"`
	ExpiresAt int64  `json:"expiresAt"`
}

type sessionClaims struct {
	Subject   string `json:"subject"`
	Email     string `json:"email"`
	ExpiresAt int64  `json:"expiresAt"`
}

func New(config Config) (*Authenticator, error) {
	if config.ClientID == "" || config.ClientSecret == "" {
		return nil, errors.New("viewer auth: Google client ID and secret are required")
	}
	redirect, err := url.Parse(config.RedirectURL)
	if err != nil || redirect.Scheme != "https" || redirect.Host == "" {
		return nil, errors.New("viewer auth: an HTTPS redirect URL is required")
	}
	if len(config.SessionKey) < sha256.Size {
		return nil, errors.New("viewer auth: session key must be at least 32 bytes")
	}
	if config.Now == nil {
		return nil, errors.New("viewer auth: clock is required")
	}

	allowed := make(map[string]struct{}, len(config.AllowedEmails))
	loginHint := ""
	for _, email := range config.AllowedEmails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || !strings.Contains(email, "@") {
			return nil, fmt.Errorf("viewer auth: invalid allowed email %q", email)
		}
		if loginHint == "" {
			loginHint = email
		}
		allowed[email] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, errors.New("viewer auth: at least one allowed email is required")
	}

	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	random := config.Random
	if random == nil {
		random = rand.Reader
	}

	return &Authenticator{
		clientID:      config.ClientID,
		clientSecret:  config.ClientSecret,
		redirectURL:   config.RedirectURL,
		allowedEmails: allowed,
		loginHint:     loginHint,
		sessionKey:    append([]byte(nil), config.SessionKey...),
		httpClient:    client,
		now:           config.Now,
		random:        random,
	}, nil
}

func (a *Authenticator) Page(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Authenticated(r) {
			location := "/auth/google/login?next=" + url.QueryEscape(r.URL.RequestURI())
			http.Redirect(w, r, location, http.StatusFound)
			return
		}
		secureHeaders(w)
		next.ServeHTTP(w, r)
	})
}

func (a *Authenticator) API(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Authenticated(r) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		secureHeaders(w)
		next.ServeHTTP(w, r)
	})
}

func (a *Authenticator) Authenticated(r *http.Request) bool {
	return a.validSession(r)
}

func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	setAuthHeaders(w)
	next := safeNext(r.URL.Query().Get("next"))
	state, err := a.randomToken()
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	claims := stateClaims{
		State:     state,
		Next:      next,
		ExpiresAt: a.now().Add(stateLifetime).Unix(),
	}
	value, err := a.sign(claims)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	setCookie(w, stateCookieName, value, stateLifetime, a.now())

	query := url.Values{
		"client_id":     {a.clientID},
		"login_hint":    {a.loginHint},
		"prompt":        {"select_account"},
		"redirect_uri":  {a.redirectURL},
		"response_type": {"code"},
		"scope":         {"openid email"},
		"state":         {state},
	}
	http.Redirect(w, r, authorizationEndpoint+"?"+query.Encode(), http.StatusFound)
}

func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) {
	setAuthHeaders(w)
	clearCookie(w, stateCookieName)
	if r.URL.Query().Get("error") != "" {
		http.Error(w, "Google sign-in was not completed", http.StatusBadRequest)
		return
	}

	claims, err := a.callbackState(r)
	if err != nil {
		http.Error(w, "Google sign-in state is invalid or expired", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Google sign-in code is missing", http.StatusBadRequest)
		return
	}

	accessToken, err := a.exchangeCode(r, code)
	if err != nil {
		http.Error(w, "Google sign-in could not be verified", http.StatusBadGateway)
		return
	}
	identity, err := a.fetchIdentity(r, accessToken)
	if err != nil {
		http.Error(w, "Google identity could not be verified", http.StatusBadGateway)
		return
	}
	email := strings.ToLower(strings.TrimSpace(identity.Email))
	if identity.Subject == "" || !identity.EmailVerified || !a.emailAllowed(email) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}

	session, err := a.sign(sessionClaims{
		Subject:   identity.Subject,
		Email:     email,
		ExpiresAt: a.now().Add(sessionLifetime).Unix(),
	})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	setCookie(w, sessionCookieName, session, sessionLifetime, a.now())
	http.Redirect(w, r, claims.Next, http.StatusFound)
}

func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	setAuthHeaders(w)
	clearCookie(w, sessionCookieName)
	clearCookie(w, stateCookieName)
	http.Redirect(w, r, "/home", http.StatusFound)
}

func (a *Authenticator) callbackState(r *http.Request) (stateClaims, error) {
	cookie, err := r.Cookie(stateCookieName)
	if err != nil {
		return stateClaims{}, err
	}
	var claims stateClaims
	if err := a.verify(cookie.Value, &claims); err != nil {
		return stateClaims{}, err
	}
	provided := r.URL.Query().Get("state")
	if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(claims.State)) != 1 {
		return stateClaims{}, errors.New("state mismatch")
	}
	if claims.ExpiresAt <= a.now().Unix() {
		return stateClaims{}, errors.New("state expired")
	}
	claims.Next = safeNext(claims.Next)
	return claims, nil
}

func (a *Authenticator) exchangeCode(r *http.Request, code string) (string, error) {
	values := url.Values{
		"client_id":     {a.clientID},
		"client_secret": {a.clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {a.redirectURL},
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, tokenEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var response struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := a.doJSON(request, &response); err != nil {
		return "", fmt.Errorf("exchange code: %w", err)
	}
	if response.AccessToken == "" || response.Error != "" {
		return "", fmt.Errorf("exchange code: %s", response.ErrorDescription)
	}
	return response.AccessToken, nil
}

type googleIdentity struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

func (a *Authenticator) fetchIdentity(r *http.Request, accessToken string) (googleIdentity, error) {
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, userinfoEndpoint, nil)
	if err != nil {
		return googleIdentity{}, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	var identity googleIdentity
	if err := a.doJSON(request, &identity); err != nil {
		return googleIdentity{}, fmt.Errorf("fetch identity: %w", err)
	}
	return identity, nil
}

func (a *Authenticator) doJSON(request *http.Request, target any) error {
	response, err := a.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxResponseBytes {
		return errors.New("response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", response.StatusCode)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (a *Authenticator) validSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	var claims sessionClaims
	if err := a.verify(cookie.Value, &claims); err != nil {
		return false
	}
	return claims.Subject != "" && claims.ExpiresAt > a.now().Unix() && a.emailAllowed(claims.Email)
}

func (a *Authenticator) emailAllowed(email string) bool {
	_, ok := a.allowedEmails[strings.ToLower(strings.TrimSpace(email))]
	return ok
}

func (a *Authenticator) randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(a.random, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (a *Authenticator) sign(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, a.sessionKey)
	_, _ = mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature, nil
}

func (a *Authenticator) verify(value string, target any) error {
	if len(value) == 0 || len(value) > maxCookieBytes {
		return errors.New("invalid signed value")
	}
	payload, encodedSignature, ok := strings.Cut(value, ".")
	if !ok {
		return errors.New("invalid signed value")
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil {
		return errors.New("invalid signature")
	}
	mac := hmac.New(sha256.New, a.sessionKey)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return errors.New("invalid signature")
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return errors.New("invalid payload")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return errors.New("invalid payload")
	}
	return nil
}

func safeNext(value string) string {
	if value == "" {
		return "/home"
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" {
		return "/home"
	}
	if !protectedPagePath(parsed.Path) {
		return "/home"
	}
	return parsed.RequestURI()
}

func protectedPagePath(value string) bool {
	if value == "/home" || value == "/wire" || value == "/agents" || value == "/settings" || value == "/triggers" || value == "/workflows" {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(value, "/"), "/")
	if len(parts) != 4 || parts[0] != "agents" || parts[3] != "run" || !issueIdentifierPattern.MatchString(parts[1]) {
		return false
	}
	started, err := strconv.ParseInt(parts[2], 10, 64)
	return err == nil && started > 0
}

func setCookie(w http.ResponseWriter, name, value string, lifetime time.Duration, now time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Expires:  now.Add(lifetime),
		MaxAge:   int(lifetime.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Path:     "/",
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func setAuthHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	secureHeaders(w)
}

func secureHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
}
