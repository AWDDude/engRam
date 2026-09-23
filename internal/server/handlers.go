package server

import (
	"context"
	"encoding/json"
	"errors"
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
	defaultLimit    int
	maxContentChars int
}

// NewApp constructs an App with the given store and default search settings.
func NewApp(s store.Store, defaultLimit, maxContentChars int) *App {
	return &App{store: s, defaultLimit: defaultLimit, maxContentChars: maxContentChars}
}

// writeFailure renders a failed write for the caller.
//
// Every Store write is all-or-nothing, so the message says outright that
// nothing was written. Without that a caller cannot tell a rejected call from
// a half-applied one, and has to go and look before it dares retry; an
// agent reading this is exactly the caller that will otherwise guess.
//
// outcome names what did not happen, e.g. "No memory was created".
func writeFailure(label string, err error, outcome string) string {
	advice := "It is safe to retry."
	var missing *store.MissingLinksError
	if errors.As(err, &missing) {
		// The IDs are already named in err's own message, so point at them
		// rather than listing them twice.
		advice = "Correct or remove those linked_ids and retry."
	}
	return fmt.Sprintf("%s: %v. %s; nothing was written. %s", label, err, outcome, advice)
}

// validateContent enforces the required, non-empty, size-capped rule shared by
// store (always) and update (when content is patched at all). The cap is a
// guardrail against pasted logs and whole documents: oversized content is
// chunked rather than rejected by the model, so the cost is a slow embed and a
// vaguer vector rather than a failure, which makes it worth catching here.
func (a *App) validateContent(content string) error {
	if content == "" {
		return fmt.Errorf("content is required")
	}
	if n := utf8.RuneCountInString(content); n > a.maxContentChars {
		return fmt.Errorf("content exceeds the %d character limit (got %d) — "+
			"split it into separate linked memories, or raise max_content_chars in the config",
			a.maxContentChars, n)
	}
	return nil
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
	TagFilter []string `json:"tag_filter"`
	Limit     *int     `json:"limit"`
	Offset    int      `json:"offset"`
}

// searchMemoryResponse wraps the result page with total, the count of
// matches before limit/offset were applied, so a paginating caller can tell
// whether it has walked to the end.
type searchMemoryResponse struct {
	Results []store.SearchResult `json:"results"`
	Total   int                  `json:"total"`
}

type retrieveMemoryArgs struct {
	MemoryID string `json:"memory_id"`
}

type deleteMemoryArgs struct {
	MemoryID string `json:"memory_id"`
}

type updateMemoryArgs struct {
	MemoryID        string    `json:"memory_id"`
	Title           *string   `json:"title"`
	Content         *string   `json:"content"`
	Tags            *[]string `json:"tags"`
	AddTags         []string  `json:"add_tags"`
	RemoveTags      []string  `json:"remove_tags"`
	LinkedIDs       *[]string `json:"linked_ids"`
	AddLinkedIDs    []string  `json:"add_linked_ids"`
	RemoveLinkedIDs []string  `json:"remove_linked_ids"`
}

func (a *App) handleStoreMemory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args storeMemoryArgs
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if err := validateTitle(args.Title); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if err := a.validateContent(args.Content); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	id, err := a.store.Add(ctx, args.Title, args.Content, args.Tags, args.LinkedIDs)
	if err != nil {
		return mcp.NewToolResultError(writeFailure("store error", err,
			"No memory was created")), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(`{"id":%q,"status":"stored"}`, id)), nil
}

func (a *App) handleSearchMemory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args searchMemoryArgs
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	limit := a.defaultLimit
	if args.Limit != nil {
		limit = *args.Limit
	}

	results, total, err := a.store.Search(ctx, args.Query, args.TagFilter, limit, args.Offset)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
	}
	if results == nil {
		results = []store.SearchResult{}
	}

	out, err := json.Marshal(searchMemoryResponse{Results: results, Total: total})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
	}
	return mcp.NewToolResultText(string(out)), nil
}

func (a *App) handleListTags(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tags, err := a.store.Tags(ctx)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("list_tags error: %v", err)), nil
	}
	if tags == nil {
		tags = []string{}
	}

	out, err := json.Marshal(tags)
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

	// The store assembles the record and its linked summaries under one read
	// lock. Walking the links from here with a GetByID each would reintroduce
	// the gaps a concurrent write can land in, which matters now that several
	// sessions share one store.
	result, err := a.store.Retrieve(ctx, args.MemoryID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("retrieve error: %v", err)), nil
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
	if args.Tags != nil && (len(args.AddTags) > 0 || len(args.RemoveTags) > 0) {
		return mcp.NewToolResultError("tags cannot be combined with add_tags or remove_tags: use tags to replace the whole set, or add_tags/remove_tags to change it incrementally"), nil
	}
	if args.LinkedIDs != nil && (len(args.AddLinkedIDs) > 0 || len(args.RemoveLinkedIDs) > 0) {
		return mcp.NewToolResultError("linked_ids cannot be combined with add_linked_ids or remove_linked_ids: use linked_ids to replace the whole set, or add_linked_ids/remove_linked_ids to change it incrementally"), nil
	}
	if args.Title == nil && args.Content == nil &&
		args.Tags == nil && len(args.AddTags) == 0 && len(args.RemoveTags) == 0 &&
		args.LinkedIDs == nil && len(args.AddLinkedIDs) == 0 && len(args.RemoveLinkedIDs) == 0 {
		return mcp.NewToolResultError("at least one of title, content, tags, add_tags, remove_tags, linked_ids, add_linked_ids, or remove_linked_ids is required"), nil
	}
	if args.Title != nil {
		if err := validateTitle(*args.Title); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}
	if args.Content != nil {
		if err := a.validateContent(*args.Content); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}

	patch := store.MemoryUpdate{
		Title:           args.Title,
		Content:         args.Content,
		Tags:            args.Tags,
		AddTags:         args.AddTags,
		RemoveTags:      args.RemoveTags,
		LinkedIDs:       args.LinkedIDs,
		AddLinkedIDs:    args.AddLinkedIDs,
		RemoveLinkedIDs: args.RemoveLinkedIDs,
	}
	if err := a.store.Update(ctx, args.MemoryID, patch); err != nil {
		return mcp.NewToolResultError(writeFailure("update error", err,
			"The memory was not modified")), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(`{"id":%q,"status":"updated"}`, args.MemoryID)), nil
}
