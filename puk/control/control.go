// Package control exposes a small authenticated HTTP control plane for Puk.
package control

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// Backend is the control surface implemented by the running Puk engine.
type Backend interface {
	Status() any
	Probe(context.Context) error
	Select(ctx context.Context, selector, tag string) error
}

// Stopper is an optional graceful-stop capability. Backends that do not
// implement it continue to satisfy Backend and receive 501 for /api/stop.
type Stopper interface {
	Stop(context.Context) error
}

// New returns the local dashboard and authenticated JSON API.
func New(backend Backend, token string) http.Handler {
	return newServer(backend, token)
}

type server struct {
	backend   Backend
	tokenHash [32]byte
	tokenSet  bool
}

func newServer(backend Backend, token string) *server {
	return &server{
		backend:   backend,
		tokenHash: sha256.Sum256([]byte(token)),
		tokenSet:  token != "",
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" && r.Method == http.MethodGet {
		serveDashboard(w)
		return
	}
	if !s.authorized(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch r.URL.Path {
	case "/api/status":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		s.status(w)
	case "/api/probe":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if !sameOriginMutation(w, r) {
			return
		}
		s.probe(w, r)
	case "/api/select":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if !sameOriginMutation(w, r) {
			return
		}
		s.selectTarget(w, r)
	case "/api/stop":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		if !sameOriginMutation(w, r) {
			return
		}
		s.stop(w, r)
	default:
		writeAPIError(w, http.StatusNotFound, "not found")
	}
}

func (s *server) authorized(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	var credential string
	valid := s.tokenSet && len(values) == 1
	if len(values) == 1 {
		scheme, value, found := strings.Cut(values[0], " ")
		valid = valid && found && strings.EqualFold(scheme, "Bearer") && value != "" && !strings.ContainsAny(value, " \t\r\n")
		if found {
			credential = value
		}
	}
	providedHash := sha256.Sum256([]byte(credential))
	match := subtle.ConstantTimeCompare(providedHash[:], s.tokenHash[:]) == 1
	return valid && match
}

func (s *server) status(w http.ResponseWriter) {
	if s.backend == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "control backend unavailable")
		return
	}
	payload, err := json.Marshal(s.backend.Status())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not encode status")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *server) probe(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := decodeJSONBody(w, r, &input, true); err != nil {
		writeDecodeError(w, err)
		return
	}
	if s.backend == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "control backend unavailable")
		return
	}
	if err := s.backend.Probe(r.Context()); err != nil {
		writeAPIError(w, http.StatusBadGateway, "probe failed")
		return
	}
	writeJSON(w, http.StatusOK, []byte(`{"ok":true}`))
}

func (s *server) selectTarget(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Selector string `json:"selector"`
		Tag      string `json:"tag"`
	}
	if err := decodeJSONBody(w, r, &input, false); err != nil {
		writeDecodeError(w, err)
		return
	}
	input.Selector = strings.TrimSpace(input.Selector)
	input.Tag = strings.TrimSpace(input.Tag)
	if input.Selector == "" || input.Tag == "" {
		writeAPIError(w, http.StatusBadRequest, "selector and tag are required")
		return
	}
	if s.backend == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "control backend unavailable")
		return
	}
	if err := s.backend.Select(r.Context(), input.Selector, input.Tag); err != nil {
		writeAPIError(w, http.StatusBadGateway, "selection failed")
		return
	}
	writeJSON(w, http.StatusOK, []byte(`{"ok":true}`))
}

func (s *server) stop(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := decodeJSONBody(w, r, &input, true); err != nil {
		writeDecodeError(w, err)
		return
	}
	stopper, ok := s.backend.(Stopper)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "stop is not supported")
		return
	}
	if err := stopper.Stop(r.Context()); err != nil {
		writeAPIError(w, http.StatusBadGateway, "stop failed")
		return
	}
	writeJSON(w, http.StatusOK, []byte(`{"ok":true}`))
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	payload, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: message})
	writeJSON(w, status, payload)
}

func writeJSON(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
