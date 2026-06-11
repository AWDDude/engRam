package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/AWDDude/engRam/internal/store"
)

// App holds shared dependencies for all tool handlers.
type App struct {
	store store.Store
}

// NewApp constructs an App with the given store.
func NewApp(s store.Store) *App {
	return &App{store: s}
}

// Argument structs — one per tool. BindArguments unmarshals the MCP request
// into these, giving us typed access and zero boilerplate per-handler.

type storeMemoryArgs struct {
	Content string   `json:"content"`
	Type    string   `json:"type"`
	Tags    []string `json:"tags"`
}

type searchMemoryArgs struct {
	Query    string   `json:"query"`
	MinScore *float64 `json:"min_score"`
}

type listMemoriesArgs struct {
	TypeFilter string `json:"type_filter"`
	TagFilter  string `json:"tag_filter"`
	Limit      int    `json:"limit"`
}

type deleteMemoryArgs struct {
	MemoryID string `json:"memory_id"`
}

type updateMemoryArgs struct {
	MemoryID string `json:"memory_id"`
	Content  string `json:"content"`
}

func (a *App) handleStoreMemory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args storeMemoryArgs
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if args.Content == "" {
		return mcp.NewToolResultError("content is required"), nil
	}
	if args.Type == "" {
		return mcp.NewToolResultError("type is required"), nil
	}
	if !validMemoryType(args.Type) {
		return mcp.NewToolResultError(fmt.Sprintf("invalid type %q: must be one of %v", args.Type, memoryTypes)), nil
	}

	id, err := a.store.Add(ctx, args.Content, args.Type, args.Tags)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("store error: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(`{"id":%q,"status":"stored"}`, id)), nil
}

func (a *App) handleSearchMemory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args searchMemoryArgs
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	if args.Query == "" {
		return mcp.NewToolResultError("query is required"), nil
	}
	minScore := float32(0.5)
	if args.MinScore != nil {
		minScore = float32(*args.MinScore)
	}
	results, err := a.store.Search(ctx, args.Query, minScore)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
	}
	if results == nil {
		results = []store.MemoryResult{}
	}

	out, err := json.Marshal(results)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
	}
	return mcp.NewToolResultText(string(out)), nil
}

func (a *App) handleListMemories(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args listMemoriesArgs
	if err := req.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}

	memories, err := a.store.List(ctx, args.TypeFilter, args.TagFilter, limit)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("list error: %v", err)), nil
	}
	if memories == nil {
		memories = []store.Memory{}
	}

	out, err := json.Marshal(memories)
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
	if args.Content == "" {
		return mcp.NewToolResultError("content is required"), nil
	}

	if err := a.store.Update(ctx, args.MemoryID, args.Content); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("update error: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf(`{"id":%q,"status":"updated"}`, args.MemoryID)), nil
}
