package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 error codes, plus MCP Events' callback error (PROTOCOL §4.4).
// ErrCodeCallback is the one place that code lives [DOTS §3].
const (
	ErrParse          = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
	ErrCodeCallback   = -32015
)

// RPCError is a JSON-RPC error object. Handlers return it to choose the
// code; any other error becomes ErrInternal.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message)
}

// Request is one decoded JSON-RPC request or notification.
type Request struct {
	Method string
	ID     json.RawMessage // nil for notifications
	Params json.RawMessage // nil when absent
	Meta   map[string]any  // params._meta, nil when absent
}

// IsNotification reports a request without an id.
func (r *Request) IsNotification() bool { return r.ID == nil }

// wireRequest is the raw shape; ID distinguishes absent from null.
type wireRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  *string         `json:"method"`
	ID      json.RawMessage `json:"id"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// resultResponse and errorResponse are the two JSON-RPC response shapes.
// They are separate types so an empty result ({}) is still written.
type resultResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

type errorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   *RPCError       `json:"error"`
}

var nullID = json.RawMessage("null")

// parsed is the outcome of decoding one POST body: a request, a client
// response to be acknowledged with 202, or a JSON-RPC error to answer with.
type parsed struct {
	req      *Request
	response bool // the body was a JSON-RPC response (server-initiated request reply)
	err      *RPCError
	errID    json.RawMessage // id to answer err with (null when unknown)
}

// parseBody decodes a single JSON-RPC message. Batches are rejected with
// ErrInvalidRequest (MCP removed them); malformed JSON is ErrParse.
func parseBody(body []byte) parsed {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if !json.Valid(trimmed) {
			return parsed{err: &RPCError{Code: ErrParse, Message: "parse error"}, errID: nullID}
		}
		return parsed{err: &RPCError{Code: ErrInvalidRequest, Message: "JSON-RPC batches are not supported"}, errID: nullID}
	}
	var w wireRequest
	if err := json.Unmarshal(trimmed, &w); err != nil {
		if !json.Valid(trimmed) {
			return parsed{err: &RPCError{Code: ErrParse, Message: "parse error"}, errID: nullID}
		}
		return parsed{err: &RPCError{Code: ErrInvalidRequest, Message: "request must be a JSON object"}, errID: nullID}
	}
	id := validID(w.ID)
	invalid := func(msg string) parsed {
		return parsed{err: &RPCError{Code: ErrInvalidRequest, Message: msg}, errID: id}
	}
	if w.JSONRPC != "2.0" {
		return invalid(`jsonrpc must be "2.0"`)
	}
	if w.Method == nil {
		if w.ID != nil && (w.Result != nil || w.Error != nil) {
			return parsed{response: true}
		}
		return invalid("method is required")
	}
	if *w.Method == "" {
		return invalid("method is required")
	}
	if w.ID != nil && id == nil {
		return parsed{err: &RPCError{Code: ErrInvalidRequest, Message: "id must be a string or a number"}, errID: nullID}
	}
	req := &Request{Method: *w.Method, ID: id}
	if p := bytes.TrimSpace(w.Params); len(p) > 0 && !bytes.Equal(p, nullID) {
		if p[0] != '{' {
			return parsed{err: &RPCError{Code: ErrInvalidParams, Message: "params must be an object"}, errID: orNull(id)}
		}
		req.Params = p
		var m struct {
			Meta map[string]any `json:"_meta"`
		}
		if err := json.Unmarshal(p, &m); err != nil {
			return parsed{err: &RPCError{Code: ErrInvalidParams, Message: "params._meta must be an object"}, errID: orNull(id)}
		}
		req.Meta = m.Meta
	}
	return parsed{req: req}
}

// validID returns raw when it is a JSON string or number, else nil.
func validID(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	switch c := raw[0]; {
	case c == '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return raw
		}
	case c == '-' || (c >= '0' && c <= '9'):
		var n json.Number
		if json.Unmarshal(raw, &n) == nil {
			return raw
		}
	}
	return nil
}

func orNull(id json.RawMessage) json.RawMessage {
	if id == nil {
		return nullID
	}
	return id
}
