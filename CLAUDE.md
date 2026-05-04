# engRam

MCP server for long-term semantic memory. Single statically-linked Go binary.

## Stack

- **Vector store**: chromem-go (embedded, persistent, file-based)
- **Embeddings**: hugot + GoMLX simplego backend (`KnightsAnalytics/all-MiniLM-L6-v2`, downloaded once from Hugging Face, no external service)
- **MCP transport**: stdio (mark3labs/mcp-go v0.50.0)

## Commands

```bash
make build    # CGO_ENABLED=0 static binary → ./engram
make install  # build + copy to ~/.claude/mcp-servers/engram/engram
make test     # go test -v -race ./...
make clean    # remove binary
```

## Migration (one-time, from LanceDB)

```bash
# 1. Export from Python server
python3 export_lancedb.py   # writes ~/.claude/memory/export.json

# 2. Import into engram (re-embeds everything with hugot)
./engram --import ~/.claude/memory/export.json
```

## Config

Config file: `config.json` beside the binary (or `ENGRAM_CONFIG_PATH` env var).

```json
{
  "model_dir": "/path/to/models",
  "db_path":   "/path/to/db"
}
```

Missing config file → all defaults apply. Partial config → unset fields keep defaults.

On first run, the embedding model (`KnightsAnalytics/all-MiniLM-L6-v2`) is downloaded from Hugging Face to `model_dir`. Subsequent starts load it from disk with no network access.

## Data layout

```
<install-dir>/
├── engram          # the binary
├── config.json     # optional config override
├── models/
│   └── KnightsAnalytics_all-MiniLM-L6-v2/   # downloaded on first run
└── db/
    ├── meta.json   # sidecar index (list/lookup without vector query)
    └── memories/   # chromem-go persistent storage (one file per doc)
```

## Architecture

```
cmd/engram/          # binary entry point
  main.go            # wires config → store → server
  import.go          # --import subcommand

internal/config/     # Config struct + Load/Default
internal/store/      # Store interface, chromemStore, metaIndex, hugot embedding
internal/server/     # App + MCP handlers + RegisterTools
```

- **`Store` interface** (`internal/store/store.go`) — all persistence behind one interface, fully mockable
- **`chromemStore`** — production: chromem-go vectors + `metaIndex` sidecar for listing
- **`metaIndex`** — JSON file of all Memory records; handles List/GetByID without a vector query
- **`App`** + **handlers** (`internal/server/`) — one method per MCP tool, uses `BindArguments` for typed arg parsing
- **`RegisterTools`** (`internal/server/tools.go`) — declarative tool schema registration

## Tools exposed

| Tool | Required args | Optional args |
|------|--------------|---------------|
| `store_memory` | content, type | tags |
| `search_memory` | query | limit (5), type_filter |
| `list_memories` | — | type_filter, tag_filter, limit (20) |
| `delete_memory` | memory_id | — |
| `update_memory` | memory_id, content | — |
