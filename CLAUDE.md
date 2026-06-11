# engRam

MCP server for long-term semantic memory. Single statically-linked Go binary.

## Stack

- **Vector store**: chromem-go (embedded, persistent, file-based)
- **Embeddings**: hugot + GoMLX simplego backend (`KnightsAnalytics/all-MiniLM-L6-v2`, downloaded once from Hugging Face, no external service)
- **MCP transport**: stdio (mark3labs/mcp-go v0.50.0)

## Commands

```bash
make build    # CGO_ENABLED=0 static binary → ./engram
make test     # go test -v -race ./...
make clean    # remove binary
```

## Config

Config file location (or override with `ENGRAM_CONFIG_PATH`):
- All platforms: `~/.config/engram/config.json`
- `XDG_CONFIG_HOME` honored on all platforms (e.g. `$XDG_CONFIG_HOME/engram/config.json`)

```json
{
  "model": {
    "path": "/path/to/models",
    "embedding_model": "KnightsAnalytics/all-MiniLM-L6-v2"
  },
  "db": {
    "path": "/path/to/db"
  }
}
```

**Warning:** changing `model.embedding_model` invalidates the vector database — all memories must be deleted and re-added.

Missing config file → created with defaults on first run. Partial/incomplete config → logs missing fields and exits.

On first run, the embedding model (`KnightsAnalytics/all-MiniLM-L6-v2`) is downloaded from Hugging Face to `model_dir`. Subsequent starts load it from disk with no network access.

## Data layout

```
~/.local/share/engram/   # all platforms ($XDG_DATA_HOME/engram if set)
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
| `store` | content, type | tags |
| `search` | query | min_score (default 0.5) |
| `list` | — | type_filter, tag_filter, limit (20) |
| `delete` | memory_id | — |
| `update` | memory_id, content | — |
