package mcpserver

import "encoding/json"

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type Citation struct {
	DocumentID  int64    `json:"document_id"`
	Title       string   `json:"title"`
	VersionID   int64    `json:"version_id"`
	SectionID   int64    `json:"section_id,omitempty"`
	HeadingPath []string `json:"heading_path,omitempty"`
	Pages       []int32  `json:"pages,omitempty"`
	TrustTier   string   `json:"trust_tier,omitempty"`
}

// ToolDefinitions returns the canonical read-only MCP tools shared by /mcp and Agentic QA.
func ToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "search",
			Description: "Hybrid search across authorized knowledge bases; returns ranked evidence with citations.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 4000},
					"kb_ids": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "integer", "minimum": 1},
						"description": "Optional knowledge base IDs; defaults to all authorized knowledge bases in scope.",
					},
					"filters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"doc_tags":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"mimes":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"updated_after": map[string]any{"type": "string", "format": "date-time"},
						},
					},
					"top_k": map[string]any{"type": "integer", "minimum": 1, "maximum": 30, "default": 8},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "list_documents",
			Description: "List visible documents in a knowledge base with optional title, tag, and status filters.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kb_id":  map[string]any{"type": "integer", "minimum": 1},
					"query":  map[string]any{"type": "string", "maxLength": 200},
					"tags":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"status": map[string]any{"type": "string", "enum": []string{"ready", "partial", "failed", "queued", "parsing", "embedding", "indexing"}},
					"page":   map[string]any{"type": "integer", "minimum": 0, "description": "Cursor document ID from previous page (0 for first page)."},
				},
				"required": []string{"kb_id"},
			},
		},
		{
			Name:        "get_toc",
			Description: "Return the table of contents (sections, headings, page ranges) for a visible document.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"document_id": map[string]any{"type": "integer", "minimum": 1},
					"cursor":      map[string]any{"type": "integer", "minimum": 0, "default": 0},
					"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 30},
				},
				"required": []string{"document_id"},
			},
		},
		{
			Name:        "read_section",
			Description: "Read the full Markdown content and citation metadata of a document section.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"section_id": map[string]any{"type": "integer", "minimum": 1},
					"max_tokens": map[string]any{"type": "integer", "minimum": 1, "maximum": 20000},
				},
				"required": []string{"section_id"},
			},
		},
		{
			Name:        "get_neighbors",
			Description: "Read neighboring chunks before and after a target chunk with citation metadata.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"chunk_id": map[string]any{"type": "integer", "minimum": 1},
					"before":   map[string]any{"type": "integer", "minimum": 0, "maximum": 10, "default": 1},
					"after":    map[string]any{"type": "integer", "minimum": 0, "maximum": 10, "default": 1},
				},
				"required": []string{"chunk_id"},
			},
		},
		{
			Name:        "exact_match",
			Description: "Exact substring or regex search across authorized chunks for clause numbers, IDs, or error codes.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern":   map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					"kb_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1}},
					"use_regex": map[string]any{"type": "boolean", "default": false},
					"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 30, "default": 10},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

// ToolsJSON returns the deterministic JSON representation for contracts/mcp-tools.json.
func ToolsJSON() ([]byte, error) {
	payload := struct {
		SchemaVersion string           `json:"schema_version"`
		Tools         []ToolDefinition `json:"tools"`
	}{
		SchemaVersion: "1.0",
		Tools:         ToolDefinitions(),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
