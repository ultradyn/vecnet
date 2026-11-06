package mcpserver

import (
	"encoding/json"
	"fmt"
	"strings"
)

// projectTool is discovery-only. buildTools has already compiled the original
// input (including its hubValues exceptions); nothing here can reach tools/call.
// Work on a JSON copy so pruning never mutates shared envelope definitions,
// output schemas or another tool's input.
func projectTool(entry map[string]any) map[string]any {
	raw, err := json.Marshal(entry)
	if err != nil {
		panic(fmt.Sprintf("mcpserver: project tool: %v", err))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(fmt.Sprintf("mcpserver: project tool: %v", err))
	}
	name := out["name"].(string)
	description, ok := toolDescriptions[name]
	if !ok {
		panic("mcpserver: missing MCP description for " + name)
	}
	out["description"] = description
	input := out["inputSchema"].(schema)
	defs, _ := input["$defs"].(schema)
	delete(input, "$defs")
	if name == "vecnet_send" {
		delete(input["properties"].(schema), "origin") // ignored for mcp_tool ingress
	}
	out["inputSchema"] = projectSchema(input, defs, name, "", false)
	if output, ok := out["outputSchema"].(schema); ok {
		out["outputSchema"] = projectSchema(output, nil, name, "", true)
	}
	// Keep the minimal OAuth declaration: ChatGPT uses securitySchemes for
	// account linking (docs/PROTOCOL.md section 4.3). This is a client extension,
	// not a base-MCP requirement, for both negotiated eras: 2026-07-28 and
	// 2025-11-25 (also the 2025-06-18 compatibility path). See
	// https://developers.openai.com/plugins/build/auth and
	// https://developers.openai.com/apps-sdk/reference/ .
	return out
}

// projectSchema retains call shapes, enums and defaults. The detailed string
// syntax and size constraints remain in the call validator and guide. Uncommon
// job/approval payloads are documented in the guide instead of expanded in
// every client's context. Output contracts keep all validation keywords.
func projectSchema(node schema, defs schema, tool, property string, output bool) schema {
	if ref, ok := node["$ref"].(string); ok {
		key := strings.TrimPrefix(ref, "#/$defs/")
		base, ok := defs[key].(schema)
		if !ok {
			panic("mcpserver: unresolved projected ref " + ref)
		}
		resolved := projectSchema(base, defs, tool, property, output)
		for k, v := range node {
			if k != "$ref" {
				resolved[k] = v
			}
		}
		node = resolved
	}
	out := schema{}
	for k, v := range node {
		switch k {
		case "description", "$comment", "title":
			continue
		case "pattern", "format", "minLength", "maxLength", "minItems", "maxItems", "minimum", "maximum", "maxProperties":
			if !output && !(property == "data_base64" && k == "maxLength") {
				continue
			}
		case "properties":
			props := schema{}
			for p, child := range v.(schema) {
				// These are server evidence, forbidden on sends by
				// attachmentReference, rather than caller arguments.
				if !output && (p == "sha256_verified" || p == "integrity") {
					continue
				}
				props[p] = projectSchema(child.(schema), defs, tool, p, output)
			}
			out[k] = props
			continue
		}
		switch child := v.(type) {
		case schema:
			out[k] = projectSchema(child, defs, tool, "", output)
		case []any:
			items := make([]any, len(child))
			for i, item := range child {
				if s, ok := item.(schema); ok {
					items[i] = projectSchema(s, defs, tool, "", output)
				} else {
					items[i] = item
				}
			}
			out[k] = items
		default:
			out[k] = v
		}
	}
	if !output {
		switch property {
		case "job_request", "job_status", "approval_request", "approval_response":
			out = schema{"type": "object"}
		}
		// The attachment's conditional rules involve hub evidence and upload
		// retention. Keep caller fields; the guide explains copying a completed
		// upload reference and the original validator still checks it in full.
		if props, ok := out["properties"].(schema); ok && props["sha256"] != nil && props["blob_id"] != nil {
			delete(out, "allOf")
			delete(out, "not")
		}
	}
	if property != "" {
		description := propertyDescriptions[property]
		if output && property == "kind" {
			description = "" // the identity enum explains this output field
		}
		if override := toolPropertyDescriptions[tool][property]; override != "" {
			description = override
		}
		if description != "" {
			out["description"] = description
		}
	}
	if !output && (out["enum"] != nil || out["const"] != nil) {
		delete(out, "type") // enum/const already fixes the value's type
	}
	return out
}
