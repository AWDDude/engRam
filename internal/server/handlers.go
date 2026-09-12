package server

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/AWDDude/engRam/internal/store"
)

// titleMaxLen is the hard, non-configurable cap on a memory's title.
const titleMaxLen = 100

// App holds shared dependencies for all tool handlers.
type App struct {
	store           store.Store
	defaultMinScore float32
	defaultLimit    int
}

// NewApp constructs an App with the given store and default search settings.
func NewApp(s store.Store, defaultMinScore float32, defaultLimit int) *App {
	return &App{store: s, defaultMinScore: defaultMinScore, defaultLimit: defaultLimit}
}

// validateTitle enforces the required, non-empty, <=titleMaxLen-character rule
// shared by store (always) and update (when a title is provided at all).
func validateTitle(title string) error {
	if title == "" {
		return fmt.Errorf("title is required")
	}
	if n := utf8.RuneCountInString(title); n > titleMaxLen {
		return fmt.Errorf("title exceeds %d characters (got %d)", titleMaxLen, n)
	}
	return nil
}

// Argument structs — one per tool. BindArguments unmarshals the MCP request
// into these, giving us typed access and zero boilerplate per-handler.

type storeMemoryArgs struct {
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	LinkedIDs []string `json:"linked_ids"`
}

type searchMemoryArgs struct {
	Query     string   `json:"query"`
	TagFilter string   `json:"tag_filter"`
	MinScore  *float64 `json:"min_score"`
	Limit     *int     `json:"limit"`
}

type retrieveMemoryArgs struct {
	MemoryID string `json:"memory_id"`
}

type deleteMemoryArgs struct {
	MemoryID string `json:"memory_id"`
}

type updateMemoryArgs struct {
	MemoryID  string    `json:"memory_id"`
	Title     *string   `json:"title"`
	Content   *string   `json:"content"`
	Tags      *[]string `json:"tags"`
	LinkedIDs *[]string `json:"linked_ids"`
}

func (a *App) handleStoreMemory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args storeMemoryArgs
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if err := validateTitle(args.Title); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if args.Content == "" {
		return mcp.NewToolResultError("content is required"), nil
	}

	id, err := a.store.Add(ctx, args.Title, args.Content, args.Tags, args.LinkedIDs)
	if err != nil {
		if id != "" {
			// The memory was persisted but linking failed partway through
			// (see store.Add); surface the id so the caller can still
			// retrieve/update/delete it instead of losing track of it.
			return mcp.NewToolResultError(fmt.Sprintf("store error: %v (memory was created with id %q)", err, id)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("store error: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(`{"id":%q,"status":"stored"}`, id)), nil
}

func (a *App) handleSearchMemory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args searchMemoryArgs
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if args.Query == "" && args.TagFilter == "" {
		return mcp.NewToolResultError("at least one of query or tag_filter is required"), nil
	}

	minScore := a.defaultMinScore
	if args.MinScore != nil {
		minScore = float32(*args.MinScore)
	}
	limit := a.defaultLimit
	if args.Limit != nil {
		limit = *args.Limit
	}

	results, err := a.store.Search(ctx, args.Query, args.TagFilter, minScore, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
	}
	if results == nil {
		results = []store.SearchResult{}
	}

	out, err := json.Marshal(results)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
	}
	return mcp.NewToolResultText(string(out)), nil
}

func (a *App) handleRetrieveMemory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args retrieveMemoryArgs
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if args.MemoryID == "" {
		return mcp.NewToolResultError("memory_id is required"), nil
	}

	mem, err := a.store.GetByID(ctx, args.MemoryID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("retrieve error: %v", err)), nil
	}

	linked := make([]store.SearchResult, 0, len(mem.LinkedIDs))
	for _, linkedID := range mem.LinkedIDs {
		linkedMem, err := a.store.GetByID(ctx, linkedID)
		if err != nil {
			// Defense-in-depth: a dangling reference (e.g. hand-edited DB)
			// shouldn't make retrieve unusable — skip it rather than error.
			continue
		}
		linked = append(linked, store.SearchResult{ID: linkedMem.ID, Title: linkedMem.Title, Tags: linkedMem.Tags})
	}

	result := store.RetrieveResult{
		ID:        mem.ID,
		Title:     mem.Title,
		Content:   mem.Content,
		Tags:      mem.Tags,
		CreatedAt: mem.CreatedAt,
		Linked:    linked,
	}
	out, err := json.Marshal(result)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
	}
	return mcp.NewToolResultText(string(out)), nil
}

func (a *App) handleDeleteMemory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args deleteMemoryArgs
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if args.MemoryID == "" {
		return mcp.NewToolResultError("memory_id is required"), nil
	}

	if err := a.store.Delete(ctx, args.MemoryID); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("delete error: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(`{"id":%q,"status":"deleted"}`, args.MemoryID)), nil
}

func (a *App) handleUpdateMemory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args updateMemoryArgs
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if args.MemoryID == "" {
		return mcp.NewToolResultError("memory_id is required"), nil
	}
	if args.Title == nil && args.Content == nil && args.Tags == nil && args.LinkedIDs == nil {
		return mcp.NewToolResultError("at least one of title, content, tags, or linked_ids is required"), nil
	}
	if args.Title != nil {
		if err := validateTitle(*args.Title); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}
	if args.Content != nil && *args.Content == "" {
		return mcp.NewToolResultError("content cannot be empty"), nil
	}

	patch := store.MemoryUpdate{
		Title:     args.Title,
		Content:   args.Content,
		Tags:      args.Tags,
		LinkedIDs: args.LinkedIDs,
	}
	if err := a.store.Update(ctx, args.MemoryID, patch); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("update error: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(`{"id":%q,"status":"updated"}`, args.MemoryID)), nil
}
