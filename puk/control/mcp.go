package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const mcpProtocolVersion = "2025-11-25"

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ServeMCP serves MCP JSON-RPC messages over newline-delimited stdio.
func ServeMCP(ctx context.Context, in io.Reader, out io.Writer, backend Backend) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		response, shouldWrite := handleMCPMessage(ctx, scanner.Bytes(), backend)
		if !shouldWrite {
			continue
		}
		payload, err := json.Marshal(response)
		if err != nil {
			return errors.New("could not encode MCP response")
		}
		payload = append(payload, '\n')
		if _, err := out.Write(payload); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP input: %w", err)
	}
	return ctx.Err()
}

func handleMCPMessage(ctx context.Context, line []byte, backend Backend) (rpcResponse, bool) {
	var raw json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return mcpError(json.RawMessage("null"), -32700, "Parse error"), true
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(raw, &message); err != nil || message == nil {
		return mcpError(json.RawMessage("null"), -32600, "Invalid Request"), true
	}
	id, hasID := message["id"]
	if hasID && !validRPCID(id) {
		return mcpError(json.RawMessage("null"), -32600, "Invalid Request"), true
	}
	if string(message["jsonrpc"]) != `"2.0"` {
		return mcpError(responseID(id, hasID), -32600, "Invalid Request"), true
	}
	var method string
	if err := json.Unmarshal(message["method"], &method); err != nil || strings.TrimSpace(method) == "" {
		return mcpError(responseID(id, hasID), -32600, "Invalid Request"), true
	}
	if !hasID {
		// MCP notifications, including notifications/initialized, have no reply.
		return rpcResponse{}, false
	}

	result, rpcErr := dispatchMCP(ctx, method, message["params"], backend)
	if rpcErr != nil {
		return mcpError(id, rpcErr.Code, rpcErr.Message), true
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}, true
}

func dispatchMCP(ctx context.Context, method string, params json.RawMessage, backend Backend) (any, *rpcError) {
	switch method {
	case "initialize":
		version, err := negotiateMCPVersion(params)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": "puk-control", "version": "0.1.0"},
		}, nil
	case "ping":
		if !optionalJSONObject(params) {
			return nil, &rpcError{Code: -32602, Message: "Invalid params"}
		}
		return map[string]any{}, nil
	case "tools/list":
		if !optionalJSONObject(params) {
			return nil, &rpcError{Code: -32602, Message: "Invalid params"}
		}
		tools := append([]mcpTool(nil), mcpTools...)
		if _, ok := backend.(Stopper); ok {
			tools = append(tools, mcpTool{Name: "stop", Description: "优雅停止本地控制服务。", InputSchema: emptyToolSchema()})
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		return callMCPTool(ctx, params, backend)
	default:
		return nil, &rpcError{Code: -32601, Message: "Method not found"}
	}
}

func negotiateMCPVersion(params json.RawMessage) (string, *rpcError) {
	var input struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if len(params) == 0 || json.Unmarshal(params, &input) != nil || input.ProtocolVersion == "" || !isJSONObject(input.Capabilities) || input.ClientInfo.Name == "" || input.ClientInfo.Version == "" {
		return "", &rpcError{Code: -32602, Message: "Invalid params"}
	}
	switch input.ProtocolVersion {
	case "2024-11-05", "2025-03-26", "2025-06-18", mcpProtocolVersion:
		return input.ProtocolVersion, nil
	default:
		return mcpProtocolVersion, nil
	}
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

var mcpTools = []mcpTool{
	{Name: "status", Description: "读取路由与隧道运行状态。", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
	{Name: "probe", Description: "执行候选线路探测。", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
	{Name: "select", Description: "为指定选择器切换目标标签。", InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"selector": map[string]string{"type": "string", "description": "选择器名称"},
			"tag":      map[string]string{"type": "string", "description": "目标标签"},
		},
		"required":             []string{"selector", "tag"},
		"additionalProperties": false,
	}},
}

func emptyToolSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func callMCPTool(ctx context.Context, params json.RawMessage, backend Backend) (any, *rpcError) {
	var input struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) == 0 || json.Unmarshal(params, &input) != nil || input.Name == "" {
		return nil, &rpcError{Code: -32602, Message: "Invalid params"}
	}
	if len(input.Arguments) > 0 && !isJSONObject(input.Arguments) {
		return nil, &rpcError{Code: -32602, Message: "Invalid params"}
	}
	args := input.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if backend == nil {
		return mcpToolFailure("Control backend unavailable."), nil
	}

	switch input.Name {
	case "status":
		if !emptyJSONObject(args) {
			return nil, &rpcError{Code: -32602, Message: "Invalid params"}
		}
		status, err := json.Marshal(backend.Status())
		if err != nil {
			return mcpToolFailure("Status unavailable."), nil
		}
		return mcpToolResult(string(status), false), nil
	case "probe":
		if !emptyJSONObject(args) {
			return nil, &rpcError{Code: -32602, Message: "Invalid params"}
		}
		if err := backend.Probe(ctx); err != nil {
			return mcpToolFailure("Probe failed."), nil
		}
		return mcpToolResult("Probe completed.", false), nil
	case "select":
		var selection struct {
			Selector string `json:"selector"`
			Tag      string `json:"tag"`
		}
		if decodeStrict(args, &selection) != nil || strings.TrimSpace(selection.Selector) == "" || strings.TrimSpace(selection.Tag) == "" {
			return nil, &rpcError{Code: -32602, Message: "Invalid params"}
		}
		if err := backend.Select(ctx, strings.TrimSpace(selection.Selector), strings.TrimSpace(selection.Tag)); err != nil {
			return mcpToolFailure("Selection failed."), nil
		}
		return mcpToolResult("Selection completed.", false), nil
	case "stop":
		if !emptyJSONObject(args) {
			return nil, &rpcError{Code: -32602, Message: "Invalid params"}
		}
		stopper, ok := backend.(Stopper)
		if !ok {
			return mcpToolFailure("Stop is not supported."), nil
		}
		if err := stopper.Stop(ctx); err != nil {
			return mcpToolFailure("Stop failed."), nil
		}
		return mcpToolResult("Stop requested.", false), nil
	default:
		return nil, &rpcError{Code: -32602, Message: "Unknown tool"}
	}
}

func mcpToolResult(text string, failed bool) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": failed,
	}
}

func mcpToolFailure(message string) map[string]any { return mcpToolResult(message, true) }

func mcpError(id json.RawMessage, code int, message string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func responseID(id json.RawMessage, hasID bool) json.RawMessage {
	if !hasID {
		return json.RawMessage("null")
	}
	return id
}

func validRPCID(id json.RawMessage) bool {
	if len(id) == 0 || !json.Valid(id) {
		return false
	}
	trimmed := bytes.TrimSpace(id)
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] == '"' {
		var value string
		return json.Unmarshal(trimmed, &value) == nil
	}
	if trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9') {
		var value json.Number
		return json.Unmarshal(trimmed, &value) == nil
	}
	return false
}

func isJSONObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value != nil
}

func emptyJSONObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return isJSONObject(raw) && json.Unmarshal(raw, &value) == nil && len(value) == 0
}

func optionalJSONObject(raw json.RawMessage) bool {
	return len(raw) == 0 || isJSONObject(raw)
}

func decodeStrict(raw json.RawMessage, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
