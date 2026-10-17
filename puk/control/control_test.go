package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testToken = "control-test-secret-value"

type testBackend struct {
	mu         sync.Mutex
	status     any
	probeErr   error
	selectErr  error
	probes     int
	selections [][2]string
}

type stoppableBackend struct {
	*testBackend
	stopCalls int
	stopErr   error
}

func (b *stoppableBackend) Stop(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopCalls++
	return b.stopErr
}

func (b *testBackend) Status() any { return b.status }

func (b *testBackend) Probe(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probes++
	return b.probeErr
}

func (b *testBackend) Select(_ context.Context, selector, tag string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.selections = append(b.selections, [2]string{selector, tag})
	return b.selectErr
}

func TestHTTPAuthDashboardAndAPI(t *testing.T) {
	backend := &testBackend{status: map[string]any{"routing": "ready", "version": "test"}}
	handler := New(backend, testToken)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	if page.Code != http.StatusOK || strings.Contains(page.Body.String(), testToken) || !strings.Contains(page.Body.String(), "type=\"password\"") {
		t.Fatalf("unexpected dashboard response: status=%d", page.Code)
	}
	if strings.Contains(page.Body.String(), "innerHTML") || strings.Contains(page.Body.String(), "localStorage") || strings.Contains(page.Body.String(), "sessionStorage") {
		t.Fatal("dashboard interpolates HTML or persists the access token")
	}
	if !strings.Contains(page.Body.String(), "HTTP 403") || !strings.Contains(page.Body.String(), "TLS 握手错误") || strings.Contains(page.Body.String(), "SSL") {
		t.Fatal("dashboard lacks the sanitized HTTP 403/TLS distinction")
	}

	unauthorized := doAPI(handler, http.MethodGet, "/api/status", "wrong", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", unauthorized.Code)
	}
	status := doAPI(handler, http.MethodGet, "/api/status", testToken, "", "")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"routing":"ready"`) {
		t.Fatalf("status response = %d %s", status.Code, status.Body.String())
	}

	selected := doAPI(handler, http.MethodPost, "/api/select", testToken, "http://127.0.0.1", `{"selector":"AUTO","tag":"direct"}`)
	if selected.Code != http.StatusOK || len(backend.selections) != 1 || backend.selections[0] != [2]string{"AUTO", "direct"} {
		t.Fatalf("selection failed: status=%d selections=%v body=%s", selected.Code, backend.selections, selected.Body.String())
	}
	probe := doAPI(handler, http.MethodPost, "/api/probe", testToken, "http://127.0.0.1", `{}`)
	if probe.Code != http.StatusOK || backend.probes != 1 {
		t.Fatalf("probe failed: status=%d probes=%d", probe.Code, backend.probes)
	}
}

func TestHTTPMutationProtectionAndMalformedBodies(t *testing.T) {
	backend := &testBackend{}
	handler := New(backend, testToken)

	crossOrigin := doAPI(handler, http.MethodPost, "/api/select", testToken, "http://attacker.example", `{"selector":"A","tag":"B"}`)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request status = %d, want 403", crossOrigin.Code)
	}
	badHost := httptest.NewRequest(http.MethodPost, "http://attacker.example/api/probe", strings.NewReader(`{}`))
	badHost.Header.Set("Authorization", "Bearer "+testToken)
	badHost.Header.Set("Origin", "http://attacker.example")
	badHostResult := httptest.NewRecorder()
	handler.ServeHTTP(badHostResult, badHost)
	if badHostResult.Code != http.StatusForbidden {
		t.Fatalf("non-loopback Host status = %d, want 403", badHostResult.Code)
	}

	cases := []struct {
		name string
		body string
	}{
		{"malformed", `{"selector":`},
		{"trailing value", `{"selector":"A","tag":"B"} {}`},
		{"unknown field", `{"selector":"A","tag":"B","token":"leak"}`},
		{"missing tag", `{"selector":"A"}`},
		{"non-object probe", `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, body := "/api/select", tc.body
			if tc.name == "non-object probe" {
				path = "/api/probe"
			}
			result := doAPI(handler, http.MethodPost, path, testToken, "http://127.0.0.1", body)
			if result.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", result.Code, result.Body.String())
			}
		})
	}
	large := `{"selector":"` + strings.Repeat("x", maxJSONBody) + `","tag":"B"}`
	tooLarge := doAPI(handler, http.MethodPost, "/api/select", testToken, "http://127.0.0.1", large)
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d, want 413", tooLarge.Code)
	}
}

func TestHTTPBackendErrorsAreSafe(t *testing.T) {
	backend := &testBackend{probeErr: errors.New("internal path and credential detail")}
	result := doAPI(New(backend, testToken), http.MethodPost, "/api/probe", testToken, "http://127.0.0.1", `{}`)
	if result.Code != http.StatusBadGateway || strings.Contains(result.Body.String(), "credential") || strings.Contains(result.Body.String(), "internal path") {
		t.Fatalf("unsafe backend error response: %d %s", result.Code, result.Body.String())
	}
}

func TestHTTPStopIsOptionalAndAuthenticated(t *testing.T) {
	unsupported := doAPI(New(&testBackend{}, testToken), http.MethodPost, "/api/stop", testToken, "http://127.0.0.1", "")
	if unsupported.Code != http.StatusNotImplemented {
		t.Fatalf("unsupported stop status = %d, want 501", unsupported.Code)
	}

	backend := &stoppableBackend{testBackend: &testBackend{}}
	handler := New(backend, testToken)
	unauthorized := doAPI(handler, http.MethodPost, "/api/stop", "wrong", "http://127.0.0.1", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized stop status = %d, want 401", unauthorized.Code)
	}
	result := doAPI(handler, http.MethodPost, "/api/stop", testToken, "http://127.0.0.1", "")
	if result.Code != http.StatusOK || backend.stopCalls != 1 {
		t.Fatalf("stop response = %d %s, calls=%d", result.Code, result.Body.String(), backend.stopCalls)
	}

	failing := &stoppableBackend{testBackend: &testBackend{}, stopErr: errors.New("private shutdown detail")}
	failure := doAPI(New(failing, testToken), http.MethodPost, "/api/stop", testToken, "http://127.0.0.1", "")
	if failure.Code != http.StatusBadGateway || strings.Contains(failure.Body.String(), "private shutdown detail") {
		t.Fatalf("unsafe stop error response: %d %s", failure.Code, failure.Body.String())
	}
}

func TestRequestHelperAndTUI(t *testing.T) {
	backend := &stoppableBackend{testBackend: &testBackend{status: map[string]string{"state": "ready"}}}
	server := httptest.NewServer(New(backend, testToken))
	defer server.Close()
	response, err := Request(context.Background(), server.URL, testToken, http.MethodGet, "/api/status", nil)
	if err != nil || !json.Valid(response) || !strings.Contains(string(response), "ready") {
		t.Fatalf("Request() = %s, %v", response, err)
	}
	var out bytes.Buffer
	if err := RunTUI(context.Background(), strings.NewReader("help\nstatus\nselect AUTO direct\nstop\nquit\n"), &out, server.URL, testToken); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "命令：") || !strings.Contains(out.String(), `"state": "ready"`) || len(backend.selections) != 1 || backend.stopCalls != 1 {
		t.Fatalf("unexpected TUI output/state: %s selections=%v stopCalls=%d", out.String(), backend.selections, backend.stopCalls)
	}
}

func TestServeMCPMethodsToolsAndNotifications(t *testing.T) {
	backend := &testBackend{status: map[string]any{"routing": "ready"}}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"probe","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"select","arguments":{"selector":"AUTO","tag":"direct"}}}`,
	}, "\n")
	var output bytes.Buffer
	if err := ServeMCP(context.Background(), strings.NewReader(input), &output, backend); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("got %d replies, want 6 (notification omitted): %s", len(lines), output.String())
	}
	var init struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    struct {
				Tools map[string]any `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil || init.ID != 1 || init.Result.ProtocolVersion != mcpProtocolVersion || init.Result.Capabilities.Tools == nil {
		t.Fatalf("invalid initialize response: %s (%v)", lines[0], err)
	}
	var listed struct {
		Result struct {
			Tools []mcpTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &listed); err != nil || len(listed.Result.Tools) != 3 {
		t.Fatalf("invalid tools/list response: %s (%v)", lines[2], err)
	}
	if backend.probes != 1 || len(backend.selections) != 1 || backend.selections[0] != [2]string{"AUTO", "direct"} {
		t.Fatalf("tool calls not dispatched: probes=%d selections=%v", backend.probes, backend.selections)
	}
}

func TestServeMCPProtocolErrorsAndSafeToolErrors(t *testing.T) {
	backend := &testBackend{probeErr: errors.New("private backend details")}
	input := strings.Join([]string{
		`{broken`,
		`{"jsonrpc":"2.0","id":"missing","method":"unknown"}`,
		`{"jsonrpc":"1.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"select","arguments":{"selector":"A"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"probe","arguments":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	}, "\n")
	var output bytes.Buffer
	if err := ServeMCP(context.Background(), strings.NewReader(input), &output, backend); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d responses, want 5: %s", len(lines), output.String())
	}
	wantCodes := []int{-32700, -32601, -32600, -32602}
	for i, code := range wantCodes {
		var got struct {
			Error *rpcError `json:"error"`
		}
		if err := json.Unmarshal([]byte(lines[i]), &got); err != nil || got.Error == nil || got.Error.Code != code {
			t.Fatalf("response %d = %s, want error code %d (%v)", i, lines[i], code, err)
		}
	}
	var toolFailure struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[4]), &toolFailure); err != nil || !toolFailure.Result.IsError || len(toolFailure.Result.Content) != 1 || strings.Contains(fmt.Sprint(toolFailure.Result.Content), "private backend details") {
		t.Fatalf("unsafe/invalid tool error: %s (%v)", lines[4], err)
	}
}

func TestMCPStopToolIsOptional(t *testing.T) {
	unsupported := &testBackend{status: map[string]string{"state": "ready"}}
	var output bytes.Buffer
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"stop","arguments":{}}}`,
	}, "\n")
	if err := ServeMCP(context.Background(), strings.NewReader(input), &output, unsupported); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	var unsupportedList struct {
		Result struct {
			Tools []mcpTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &unsupportedList); err != nil || len(unsupportedList.Result.Tools) != 3 {
		t.Fatalf("unsupported backend advertised stop: %s (%v)", lines[0], err)
	}
	var unsupportedCall struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &unsupportedCall); err != nil || !unsupportedCall.Result.IsError {
		t.Fatalf("unsupported stop call was not a tool error: %s (%v)", lines[1], err)
	}

	backend := &stoppableBackend{testBackend: &testBackend{}}
	output.Reset()
	if err := ServeMCP(context.Background(), strings.NewReader(input), &output, backend); err != nil {
		t.Fatal(err)
	}
	lines = strings.Split(strings.TrimSpace(output.String()), "\n")
	var supportedList struct {
		Result struct {
			Tools []mcpTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &supportedList); err != nil || len(supportedList.Result.Tools) != 4 || supportedList.Result.Tools[3].Name != "stop" {
		t.Fatalf("supported backend did not advertise stop: %s (%v)", lines[0], err)
	}
	var supportedCall struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &supportedCall); err != nil || supportedCall.Result.IsError || backend.stopCalls != 1 {
		t.Fatalf("stop tool failed: %s (%v), calls=%d", lines[1], err, backend.stopCalls)
	}
}

func doAPI(handler http.Handler, method, path, token, origin, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	return result
}
