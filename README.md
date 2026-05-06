# engRam

A long-term semantic memory MCP server — single statically-linked Go binary with no external services required.

**Name:** *engram* (a memory trace in neuroscience) + *RAM* — stores memories, fast.

## Features

- **Single binary** — no Python, no Docker, no runtime dependencies
- **Local embeddings** via [hugot](https://github.com/knights-analytics/hugot) + GoMLX (`all-MiniLM-L6-v2`, downloaded once on first run)
- **Persistent vector search** via [chromem-go](https://github.com/philippgille/chromem-go) (embedded, file-based)
- **5 MCP tools** — store, search, list, update, delete
- **Semantic search** with optional type filtering
- **Config file** support with sane defaults

## Prerequisites

- [Go 1.23+](https://go.dev/dl/)
- Internet access on first run (to download the embedding model from Hugging Face — ~90MB, one-time)

## Installation

```bash
git clone https://github.com/AWDDude/engRam.git
cd engRam
make install   # builds and copies to ~/.claude/mcp-servers/engram/engram
```

Or just build:
```bash
make build     # produces ./engram (static binary, CGO_ENABLED=0)
```

## MCP configuration

Add to your Claude MCP config (e.g. `~/.claude/.mcp.json`):

```json
{
  "engram": {
    "command": "/path/to/engram"
  }
}
```

## Configuration

engRam looks for `config.json` beside the binary. Override the path with `ENGRAM_CONFIG_PATH`.

```json
{
  "model_dir": "/path/to/models",
  "db_path":   "/path/to/db"
}
```

All fields are optional — missing fields keep their defaults. A missing config file is not an error.

On first run, the embedding model (`KnightsAnalytics/all-MiniLM-L6-v2`) is downloaded to `model_dir`. Subsequent starts load it from disk with no network access.

## Tools

| Tool | Required | Optional |
|------|----------|----------|
| `store_memory` | `content`, `type` | `tags` |
| `search_memory` | `query` | `limit` (default 5), `type_filter` |
| `list_memories` | — | `type_filter`, `tag_filter`, `limit` (default 20) |
| `delete_memory` | `memory_id` | — |
| `update_memory` | `memory_id`, `content` | — |

**Memory types:** `user_preference`, `work_context`, `task`, `fact`, `feedback`

### Examples

```
store_memory(content="prefers dark mode", type="user_preference", tags=["ui"])
search_memory(query="UI preferences", limit=3)
list_memories(type_filter="fact", tag_filter="kubernetes")
update_memory(memory_id="<id>", content="updated content")
delete_memory(memory_id="<id>")
```

## Data layout

```
<install-dir>/
├── engram          # the binary
├── config.json     # optional config override
├── models/
│   └── KnightsAnalytics_all-MiniLM-L6-v2/   # downloaded on first run
└── db/
    ├── meta.json   # sidecar index for fast list/lookup
    └── memories/   # chromem-go vector storage (one file per document)
```

## Development

```bash
make test    # go test -v -race ./...
make build   # CGO_ENABLED=0 static binary
make clean   # remove binary
```

Tests use an in-memory store with a deterministic mock embedding function — no model download required.

## License

MIT
