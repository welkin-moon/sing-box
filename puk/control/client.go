package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	statusRequestTimeout  = 15 * time.Second
	controlRequestTimeout = 10 * time.Minute
)

// Request calls the local HTTP control API and returns its JSON response.
// Status reads have a short timeout; control operations can include a full
// batch of candidate probes and are allowed up to ten minutes.
func Request(ctx context.Context, baseURL, token, method, path string, body any) (json.RawMessage, error) {
	endpoint, err := requestURL(baseURL, path)
	if err != nil {
		return nil, errors.New("invalid control API URL")
	}

	var requestBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, errors.New("could not encode control request")
		}
		requestBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return nil, errors.New("could not create control request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	timeout := controlRequestTimeout
	if method == http.MethodGet && path == "/api/status" {
		timeout = statusRequestTimeout
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("control API request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("invalid control API response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return nil, fmt.Errorf("control API returned HTTP %d: %s", resp.StatusCode, apiErr.Error)
		}
		return nil, fmt.Errorf("control API returned HTTP %d", resp.StatusCode)
	}
	if !json.Valid(data) {
		return nil, errors.New("invalid control API response")
	}
	return json.RawMessage(data), nil
}

func requestURL(baseURL, path string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("invalid base URL")
	}
	ref, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || ref.Scheme != "" || ref.Host != "" {
		return "", errors.New("invalid request path")
	}
	return base.ResolveReference(ref).String(), nil
}
