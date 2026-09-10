package server

import (
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// RegisterTools registers all MCP tools on the given server.
func RegisterTools(s *mcpserver.MCPServer, app *App) {
	s.AddTool(
		mcp.NewTool("store",
			mcp.WithDescription("Store a new memory in engram"),
			mcp.WithString("title",
				mcp.Required(),
				mcp.Description("A short title/summary for the memory (max 100 characters)"),
			),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("The content to store"),
			),
			mcp.WithArray("tags",
				mcp.Description("Optional tags for categorization"),
				mcp.WithStringItems(),
			),
			mcp.WithArray("linked_ids",
				mcp.Description("Optional IDs of other memories to link to (linking is bidirectional)"),
				mcp.WithStringItems(),
			),
		),
		app.handleStoreMemory,
	)

	s.AddTool(
		mcp.NewTool("search",
			mcp.WithDescription("Search stored memories by semantic query and/or tag filter. At least one of query or tag_filter is required. Returns only id, title, and tags for each match — use retrieve for full details."),
			mcp.WithString("query",
				mcp.Description("Semantic search query (matches title and content)"),
			),
			mcp.WithString("tag_filter",
				mcp.Description("Filter by tag (case-insensitive substring match)"),
			),
			mcp.WithNumber("min_score",
				mcp.Description("Minimum cosine similarity threshold (0–1); only applies when query is given; overrides the configured default"),
			),
			mcp.WithInteger("limit",
				mcp.Description("Maximum results to return (0 or omitted = configured default; negative = unlimited)"),
			),
		),
		app.handleSearchMemory,
	)

	s.AddTool(
		mcp.NewTool("retrieve",
			mcp.WithDescription("Retrieve full details of a memory by ID, including titles and tags of any linked memories"),
			mcp.WithString("memory_id",
				mcp.Required(),
				mcp.Description("The ID of the memory to retrieve"),
			),
		),
		app.handleRetrieveMemory,
	)

	s.AddTool(
		mcp.NewTool("delete",
			mcp.WithDescription("Delete a memory by ID"),
			mcp.WithString("memory_id",
				mcp.Required(),
				mcp.Description("The ID of the memory to delete"),
			),
		),
		app.handleDeleteMemory,
	)

	s.AddTool(
		mcp.NewTool("update",
			mcp.WithDescription("Update an existing memory. Only the provided fields are changed; at least one must be given."),
			mcp.WithString("memory_id",
				mcp.Required(),
				mcp.Description("The ID of the memory to update"),
			),
			mcp.WithString("title",
				mcp.Description("New title (max 100 characters)"),
			),
			mcp.WithString("content",
				mcp.Description("New content"),
			),
			mcp.WithArray("tags",
				mcp.Description("New tags (replaces the existing set)"),
				mcp.WithStringItems(),
			),
			mcp.WithArray("linked_ids",
				mcp.Description("New linked memory IDs (replaces the existing set; linking is bidirectional)"),
				mcp.WithStringItems(),
			),
		),
		app.handleUpdateMemory,
	)
}
