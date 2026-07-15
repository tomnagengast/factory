package viewerauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalAuthorizationRequiresCanonicalLoopbackAuthority(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		origin    string
		fetchSite string
		want      int
	}{
		{name: "canonical", host: "127.0.0.1:8092", want: http.StatusNoContent},
		{name: "canonical origin", host: "127.0.0.1:8092", origin: "http://127.0.0.1:8092", want: http.StatusNoContent},
		{name: "alternate localhost", host: "localhost:8092", want: http.StatusForbidden},
		{name: "missing port", host: "127.0.0.1", want: http.StatusForbidden},
		{name: "wrong port", host: "127.0.0.1:8093", want: http.StatusForbidden},
		{name: "attacker host", host: "attacker.example:8092", want: http.StatusForbidden},
		{name: "host user info", host: "attacker@127.0.0.1:8092", want: http.StatusForbidden},
		{name: "cross origin", host: "127.0.0.1:8092", origin: "https://attacker.example", want: http.StatusForbidden},
		{name: "alternate origin host", host: "127.0.0.1:8092", origin: "http://localhost:8092", want: http.StatusForbidden},
		{name: "cross site fetch", host: "127.0.0.1:8092", fetchSite: "cross-site", want: http.StatusForbidden},
	}
	auth, err := NewLocal("127.0.0.1", 8092)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := auth.API(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8092/api/settings", nil)
			request.Host = test.host
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}

func TestLocalAuthorizationNormalizesIPv6AndDefaultHTTPPort(t *testing.T) {
	auth, err := NewLocal("::1", 80)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"[::1]", "[0:0:0:0:0:0:0:1]:80"} {
		request := httptest.NewRequest(http.MethodGet, "http://[::1]/settings", nil)
		request.Host = host
		request.Header.Set("Origin", "http://[::1]")
		response := httptest.NewRecorder()
		auth.Page(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("host %q status = %d", host, response.Code)
		}
	}
}

func TestLocalAuthorizationRejectsNonLoopbackConstruction(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "::", "factory.example"} {
		if _, err := NewLocal(host, 8092); err == nil {
			t.Fatalf("non-loopback host %q was accepted", host)
		}
	}
}
