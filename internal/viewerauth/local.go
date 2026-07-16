package viewerauth

import (
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/tomnagengast/factory/internal/taskstore"
)

type Local struct {
	host string
	port int
}

func NewLocal(host string, port int) (*Local, error) {
	normalized, ok := localLoopbackHost(host)
	if !ok || port < 1 || port > 65535 {
		return nil, errors.New("viewer auth: local authorization requires a loopback host and valid port")
	}
	return &Local{host: normalized, port: port}, nil
}

func (a *Local) Page(next http.Handler) http.Handler {
	return a.protect(next)
}

func (a *Local) API(next http.Handler) http.Handler {
	return a.protect(next)
}

func (a *Local) Actor(r *http.Request) (taskstore.Actor, bool) {
	if !a.authorized(r) {
		return taskstore.Actor{}, false
	}
	return taskstore.Actor{ID: "local-operator", Kind: taskstore.AuthorHuman}, true
}

func (a *Local) Login(w http.ResponseWriter, r *http.Request) {
	setAuthHeaders(w)
	if !a.authorized(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
}

func (a *Local) Callback(w http.ResponseWriter, r *http.Request) {
	setAuthHeaders(w)
	if !a.authorized(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	http.Redirect(w, r, "/home", http.StatusFound)
}

func (a *Local) Logout(w http.ResponseWriter, r *http.Request) {
	setAuthHeaders(w)
	if !a.authorized(r) {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	http.Redirect(w, r, "/home", http.StatusFound)
}

func (a *Local) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(r) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		secureHeaders(w)
		next.ServeHTTP(w, r)
	})
}

func (a *Local) authorized(r *http.Request) bool {
	host, port, ok := requestAuthority(r.Host, 80)
	if !ok || host != a.host || port != a.port {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	originHost, originPort, ok := requestAuthority(parsed.Host, 80)
	return ok && originHost == a.host && originPort == a.port
}

func requestAuthority(value string, defaultPort int) (string, int, bool) {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "/\\?#@") {
		return "", 0, false
	}
	parsed, err := url.Parse("http://" + value)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", 0, false
	}
	host, ok := normalizeAuthorityHost(parsed.Hostname())
	if !ok {
		return "", 0, false
	}
	port := defaultPort
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", 0, false
		}
	}
	return host, port, true
}

func localLoopbackHost(value string) (string, bool) {
	host, ok := normalizeAuthorityHost(value)
	if !ok {
		return "", false
	}
	if strings.EqualFold(host, "localhost") {
		return host, true
	}
	address, err := netip.ParseAddr(host)
	return host, err == nil && address.IsLoopback()
}

func normalizeAuthorityHost(value string) (string, bool) {
	if address, err := netip.ParseAddr(value); err == nil {
		return address.String(), true
	}
	if value == "" || value != strings.TrimSpace(value) || strings.Contains(value, ":") {
		return "", false
	}
	return strings.ToLower(value), true
}
