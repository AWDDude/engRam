# engRam

A long-term semantic memory MCP server — single statically-linked Go binary with no external services required.

**Name:** *engram* (a memory trace in neuroscience) + *RAM* — stores memories, fast.

## Features

- **Single binary** — no Python, no Docker, no runtime dependencies
- **Local embeddings** via [hugot](https://github.com/knights-analytics/hugot) + GoMLX ([all-MiniLM-L6-v2](https://huggingface.co/KnightsAnalytics/all-MiniLM-L6-v2), Apache 2.0, downloaded once on first run)
- **Persistent vector search** via [chromem-go](https://github.com/philippgille/chromem-go) (embedded, file-based)
- **5 MCP tools** — store, search, list, update, delete
- **Semantic search** with optional type filtering
- **XDG-compliant** data and config paths on all platforms

## Installation

### Homebrew (macOS / Linux)

```bash
brew install AWDDude/tap/engram
```

### Build from source

```bash
git clone https://github.com/AWDDude/engRam.git
cd engRam
make build     # produces ./engram (static binary, CGO_ENABLED=0)
```

Then copy the binary somewhere on your PATH, e.g. `~/.local/bin/`.

> Internet access is required on first run to download the embedding model from Hugging Face (~90MB, one-time only).

## MCP configuration

Add to `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "engram": {
      "command": "engram"
    }
  }
}
```

If you built from source or installed to a custom path, use the full path to the binary instead.

## Configuration

engRam stores data in platform-appropriate directories by default:

| Platform | Data (db, models) | Config |
|----------|-------------------|--------|
| macOS | `~/Library/Application Support/engram/` | `~/Library/Application Support/engram/config.json` |
| Linux | `~/.local/share/engram/` | `~/.config/engram/config.json` |
| Windows | `%APPDATA%\engram\` | `%APPDATA%\engram\config.json` |

`XDG_DATA_HOME` and `XDG_CONFIG_HOME` are honored on all platforms. Override the config path entirely with `ENGRAM_CONFIG_PATH`.

The config file is optional — missing fields keep their defaults, and a missing file is not an error:

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

> **Warning:** changing `model.embedding_model` invalidates your existing vector database. All stored memories must be deleted and re-added after switching models, as embeddings from different models are incompatible.

## Tools

| Tool | Required | Optional |
|------|----------|----------|
| `store_memory` | `content`, `type` | `tags` |
| `search_memory` | `query` | `limit` (omit for all), `type_filter` |
| `list_memories` | — | `type_filter`, `tag_filter`, `limit` (default 20) |
| `delete_memory` | `memory_id` | — |
| `update_memory` | `memory_id`, `content` | — |

**Memory types:** `preference`, `task`, `fact`, `action`

### Examples

```
store_memory(content="prefers dark mode", type="preference", tags=["ui"])
search_memory(query="UI preferences", limit=3)
list_memories(type_filter="fact", tag_filter="kubernetes")
update_memory(memory_id="<id>", content="updated content")
delete_memory(memory_id="<id>")
```

## Development

```bash
make test    # go test -v -race ./...
make build   # CGO_ENABLED=0 static binary
make clean   # remove binary
```

Tests use an in-memory store with a deterministic mock embedding function — no model download required.

## License

MIT — see [LICENSE](LICENSE).
