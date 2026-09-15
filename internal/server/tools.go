package server

import (
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// contentDescription documents the content argument's size cap, shared by
// store (required) and update (optional) since both enforce the same limit
// via App.validateContent.
func contentDescription(lead string, maxContentChars int) string {
	return fmt.Sprintf(
		"%s (max %d characters). Longer material belongs in "+
			"several linked memories rather than one: a memory past this size is "+
			"split into chunks and its embedding gets vaguer, making it harder to "+
			"retrieve, not easier.", lead, maxContentChars)
}

// searchLimitDescription documents the limit argument, naming the actual
// configured default rather than an abstract "the default".
//
// Two things are easy to get backwards and so are stated outright: omitting
// the argument is the capped option while 0 is the uncapped one (the reverse
// of the usual "leave it blank for everything" convention), and a short result
// list is expected rather than a sign the limit was too low, since weak
// matches are dropped by relevance before the limit is ever applied.
func searchLimitDescription(defaultLimit int) string {
	capped := fmt.Sprintf("%d", defaultLimit)
	if defaultLimit <= 0 {
		capped = "no cap, as configured"
	}
	return fmt.Sprintf(
		"Maximum results to return. Omit to use the configured default (%s). "+
			"Pass 0 to remove the cap and return every match; negative values behave the same as 0. "+
			"Note that omitting this argument is the capped option and 0 is the uncapped one. "+
			"Getting back fewer results than the limit is normal and does not mean the limit was too low: "+
			"low-relevance matches are dropped before the limit is applied, so raising it will not surface them.",
		capped,
	)
}

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
				mcp.Description(contentDescription("The content to store", app.maxContentChars)),
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
			mcp.WithDescription("Search stored memories by hybrid semantic + keyword query and/or tag filter. Omitting both query and tag_filter lists every memory, newest first, with no relevance filtering — use this to enumerate the store (audits, dedup checks, verifying a bulk operation touched everything). Results are ranked by relevance when a query is given. Returns id, title, and tags for each match plus a total count — use retrieve for full details."),
			mcp.WithString("query",
				mcp.Description("Search query (matches title and content, both semantically and by keyword). Omit along with tag_filter to list everything."),
			),
			mcp.WithArray("tag_filter",
				mcp.Description("Filter by tag: exact, case-insensitive match (not substring). Pass one or more tags — a memory must have every listed tag to match (AND). Use the list_tags tool to discover valid tag values."),
				mcp.WithStringItems(),
			),
			mcp.WithInteger("limit",
				mcp.Description(searchLimitDescription(app.defaultLimit)),
			),
			mcp.WithInteger("offset",
				mcp.Description("Number of matches to skip before returning results, for paging through a large result set. Default 0. The response's total field is the count of matches before offset/limit are applied, so offset+len(results) >= total means there are no more pages."),
			),
		),
		app.handleSearchMemory,
	)

	s.AddTool(
		mcp.NewTool("list_tags",
			mcp.WithDescription("List every distinct tag currently used across stored memories, sorted alphabetically. Use this to discover valid tag_filter values before searching."),
		),
		app.handleListTags,
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
				mcp.Description(contentDescription("New content", app.maxContentChars)),
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
