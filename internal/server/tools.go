package server

import (
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

var memoryTypes = []string{"preference", "task", "fact", "action"}

func validMemoryType(t string) bool {
	for _, mt := range memoryTypes {
		if mt == t {
			return true
		}
	}
	return false
}

// RegisterTools registers all five MCP tools on the given server.
func RegisterTools(s *mcpserver.MCPServer, app *App) {
	s.AddTool(
		mcp.NewTool("store_memory",
			mcp.WithDescription("Store a new memory in engram"),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("The content to store"),
			),
			mcp.WithString("type",
				mcp.Required(),
				mcp.Description("Memory type"),
				mcp.Enum(memoryTypes...),
			),
			mcp.WithArray("tags",
				mcp.Description("Optional tags for categorization"),
				mcp.WithStringItems(),
			),
		),
		app.handleStoreMemory,
	)

	s.AddTool(
		mcp.NewTool("search_memory",
			mcp.WithDescription("Semantically search stored memories using vector similarity"),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("The search query"),
			),
			mcp.WithInteger("limit",
				mcp.Description("Maximum results to return (omit for all results)"),
			),
			mcp.WithString("type_filter",
				mcp.Description("Restrict results to a specific memory type"),
				mcp.Enum(memoryTypes...),
			),
		),
		app.handleSearchMemory,
	)

	s.AddTool(
		mcp.NewTool("list_memories",
			mcp.WithDescription("List stored memories with optional type and tag filtering"),
			mcp.WithString("type_filter",
				mcp.Description("Filter by memory type"),
				mcp.Enum(memoryTypes...),
			),
			mcp.WithString("tag_filter",
				mcp.Description("Filter by tag (case-insensitive substring match)"),
			),
			mcp.WithInteger("limit",
				mcp.Description("Maximum results to return (default 20)"),
			),
		),
		app.handleListMemories,
	)

	s.AddTool(
		mcp.NewTool("delete_memory",
			mcp.WithDescription("Delete a memory by ID"),
			mcp.WithString("memory_id",
				mcp.Required(),
				mcp.Description("The ID of the memory to delete"),
			),
		),
		app.handleDeleteMemory,
	)

	s.AddTool(
		mcp.NewTool("update_memory",
			mcp.WithDescription("Update the content of an existing memory and re-embed it"),
			mcp.WithString("memory_id",
				mcp.Required(),
				mcp.Description("The ID of the memory to update"),
			),
			mcp.WithString("content",
				mcp.Required(),
				mcp.Description("The new content (type and tags are preserved)"),
			),
		),
		app.handleUpdateMemory,
	)
}
