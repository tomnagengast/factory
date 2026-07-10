package viewerauth

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

var authTestNow = time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)

func TestPageRedirectsToGoogleLoginAndAPIReturnsUnauthorized(t *testing.T) {
	t.Parallel()
	auth := testAuthenticator(t, nil)

	page := httptest.NewRecorder()
	auth.Page(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected page should not run")
	})).ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/agents/run-123?window=%401", nil))
	if page.Code != http.StatusFound {
		t.Fatalf("page status = %d, want %d", page.Code, http.StatusFound)
	}
	location, err := url.Parse(page.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if location.Path != "/auth/google/login" || location.Query().Get("next") != "/agents/run-123?window=%401" {
		t.Fatalf("redirect = %q", location.String())
	}

	api := httptest.NewRecorder()
	auth.API(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected API should not run")
	})).ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/agents/run-123", nil))
	if api.Code != http.StatusUnauthorized || api.Header().Get("WWW-Authenticate") != basicRealm {
		t.Fatalf("API response = %d, challenge %q", api.Code, api.Header().Get("WWW-Authenticate"))
	}
}

func TestGoogleCallbackCreatesSessionForAllowedVerifiedEmail(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case tokenEndpoint:
			if err := request.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			if request.Form.Get("code") != "google-code" || request.Form.Get("client_secret") != "google-secret" {
				t.Fatalf("token form = %v", request.Form)
			}
			return jsonResponse(http.StatusOK, `{"access_token":"access-token"}`), nil
		case userinfoEndpoint:
			if got := request.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("authorization = %q", got)
			}
			return jsonResponse(http.StatusOK, `{"sub":"google-subject","email":"Tom@Example.com","email_verified":true}`), nil
		default:
			t.Fatalf("unexpected request: %s", request.URL)
			return nil, nil
		}
	})}
	auth := testAuthenticator(t, client)

	stateCookie, state := beginLogin(t, auth, "/agents/run-123")
	callback := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/auth/google/callback?state="+url.QueryEscape(state)+"&code=google-code",
		nil,
	)
	request.AddCookie(stateCookie)
	auth.Callback(callback, request)
	if callback.Code != http.StatusFound || callback.Header().Get("Location") != "/agents/run-123" {
		t.Fatalf("callback = %d, location %q, body %q", callback.Code, callback.Header().Get("Location"), callback.Body.String())
	}
	if !responseDeletesCookie(callback, stateCookieName) {
		t.Fatal("callback did not clear the OAuth state cookie")
	}
	session := responseCookie(t, callback, sessionCookieName)
	if !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %#v", session)
	}

	protected := httptest.NewRecorder()
	protectedRequest := httptest.NewRequest(http.MethodGet, "/agents/run-123", nil)
	protectedRequest.AddCookie(session)
	auth.Page(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(protected, protectedRequest)
	if protected.Code != http.StatusNoContent {
		t.Fatalf("protected status = %d", protected.Code)
	}
	if protected.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing protected security headers")
	}
}

func TestGoogleCallbackRejectsUnlistedEmail(t *testing.T) {
	t.Parallel()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == tokenEndpoint {
			return jsonResponse(http.StatusOK, `{"access_token":"access-token"}`), nil
		}
		return jsonResponse(http.StatusOK, `{"sub":"other-subject","email":"other@example.com","email_verified":true}`), nil
	})}
	auth := testAuthenticator(t, client)
	stateCookie, state := beginLogin(t, auth, "/agents/run-123")

	callback := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/auth/google/callback?state="+state+"&code=google-code", nil)
	request.AddCookie(stateCookie)
	auth.Callback(callback, request)
	if callback.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", callback.Code, http.StatusForbidden)
	}
}

func TestBreakGlassBasicAuthenticationStillWorks(t *testing.T) {
	t.Parallel()
	auth := testAuthenticator(t, nil)
	request := httptest.NewRequest(http.MethodGet, "/agents/run-123", nil)
	request.SetBasicAuth("factory", "viewer-password")
	recorder := httptest.NewRecorder()
	auth.Page(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestCallbackRejectsTamperedState(t *testing.T) {
	t.Parallel()
	auth := testAuthenticator(t, nil)
	stateCookie, state := beginLogin(t, auth, "/agents/run-123")
	stateCookie.Value += "tampered"
	request := httptest.NewRequest(http.MethodGet, "/auth/google/callback?state="+state+"&code=code", nil)
	request.AddCookie(stateCookie)
	recorder := httptest.NewRecorder()
	auth.Callback(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestLoginRejectsExternalReturnURL(t *testing.T) {
	t.Parallel()
	auth := testAuthenticator(t, nil)
	cookie, state := beginLogin(t, auth, "https://attacker.example/steal")
	var claims stateClaims
	if err := auth.verify(cookie.Value, &claims); err != nil {
		t.Fatalf("verify state: %v", err)
	}
	if claims.State != state || claims.Next != "/activity" {
		t.Fatalf("state claims = %#v", claims)
	}
}

func beginLogin(t *testing.T, auth *Authenticator, next string) (*http.Cookie, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/auth/google/login?next="+url.QueryEscape(next), nil)
	auth.Login(recorder, request)
	if recorder.Code != http.StatusFound {
		t.Fatalf("login status = %d, body %q", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Google redirect: %v", err)
	}
	if location.Scheme != "https" || location.Host != "accounts.google.com" {
		t.Fatalf("Google redirect = %q", location.String())
	}
	if location.Query().Get("client_id") != "google-client" || location.Query().Get("redirect_uri") != "https://factory.example/auth/google/callback" {
		t.Fatalf("Google query = %v", location.Query())
	}
	state := location.Query().Get("state")
	if state == "" {
		t.Fatal("Google redirect is missing state")
	}
	return responseCookie(t, recorder, stateCookieName), state
}

func responseCookie(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name && cookie.MaxAge >= 0 {
			return cookie
		}
	}
	t.Fatalf("response cookie %q not found", name)
	return nil
}

func responseDeletesCookie(recorder *httptest.ResponseRecorder, name string) bool {
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name && cookie.MaxAge < 0 {
			return true
		}
	}
	return false
}

func testAuthenticator(t *testing.T, client *http.Client) *Authenticator {
	t.Helper()
	auth, err := New(Config{
		ClientID:      "google-client",
		ClientSecret:  "google-secret",
		RedirectURL:   "https://factory.example/auth/google/callback",
		AllowedEmails: []string{"tom@example.com"},
		SessionKey:    bytes.Repeat([]byte("s"), 32),
		BasicUsername: "factory",
		BasicPassword: "viewer-password",
		HTTPClient:    client,
		Now:           func() time.Time { return authTestNow },
		Random:        strings.NewReader(strings.Repeat("r", 64)),
	})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	return auth
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
