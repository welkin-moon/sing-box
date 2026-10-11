package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxJSONBody = 64 << 10

var (
	errBodyTooLarge  = errors.New("request body too large")
	errMalformedJSON = errors.New("malformed JSON")
)

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) error {
	if r.Body == nil {
		if allowEmpty {
			return nil
		}
		return errMalformedJSON
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errBodyTooLarge
		}
		return errMalformedJSON
	}
	if len(body) == 0 {
		if allowEmpty {
			return nil
		}
		return errMalformedJSON
	}
	if len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '{' {
		return errMalformedJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return errMalformedJSON
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errMalformedJSON
	}
	return nil
}

func writeDecodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	writeAPIError(w, http.StatusBadRequest, "invalid JSON body")
}

// sameOriginMutation blocks browser cross-origin writes. Requests without an
// Origin are useful to non-browser clients, but must still carry a valid Host.
func sameOriginMutation(w http.ResponseWriter, r *http.Request) bool {
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	defaultPort := "80"
	if scheme == "https" {
		defaultPort = "443"
	}
	requestHost, ok := canonicalHost(host, defaultPort)
	if !ok || !isLoopbackHost(host) {
		writeAPIError(w, http.StatusForbidden, "invalid request host")
		return false
	}

	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			writeAPIError(w, http.StatusForbidden, "cross-origin request denied")
			return false
		}
		return true
	}
	if len(origins) != 1 {
		writeAPIError(w, http.StatusForbidden, "cross-origin request denied")
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		writeAPIError(w, http.StatusForbidden, "cross-origin request denied")
		return false
	}
	originScheme := strings.ToLower(origin.Scheme)
	if originScheme != "http" && originScheme != "https" {
		writeAPIError(w, http.StatusForbidden, "cross-origin request denied")
		return false
	}
	originDefaultPort := "80"
	if originScheme == "https" {
		originDefaultPort = "443"
	}
	originHost, ok := canonicalHost(origin.Host, originDefaultPort)
	if !ok || originScheme != scheme || originHost != requestHost {
		writeAPIError(w, http.StatusForbidden, "cross-origin request denied")
		return false
	}
	return true
}

func isLoopbackHost(raw string) bool {
	u, err := url.Parse("//" + raw)
	if err != nil {
		return false
	}
	name := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if name == "localhost" {
		return true
	}
	ip := net.ParseIP(name)
	return ip != nil && ip.IsLoopback()
}

func canonicalHost(raw, defaultPort string) (string, bool) {
	if raw == "" || strings.ContainsAny(raw, "/?#@ \t\r\n") {
		return "", false
	}
	u, err := url.Parse("//" + raw)
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	name := u.Hostname()
	if name == "" {
		return "", false
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	} else {
		parsed, err := strconv.Atoi(port)
		if err != nil || parsed < 1 || parsed > 65535 {
			return "", false
		}
		port = strconv.Itoa(parsed)
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if ip := net.ParseIP(name); ip != nil {
		name = ip.String()
	}
	return net.JoinHostPort(name, port), true
}
