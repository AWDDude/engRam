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
- **Model migration** — switch embedding models without losing memories

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

engRam uses XDG-style directories by default on all platforms:

| Purpose | Default path |
|---------|-------------|
| Data (db, models) | `~/.local/share/engram/` |
| Config | `~/.config/engram/config.json` |

`XDG_DATA_HOME` and `XDG_CONFIG_HOME` are honored on all platforms. Override the config path entirely with `ENGRAM_CONFIG_PATH`.

If the config file does not exist, engram creates it with defaults on first run. All fields are required — engram will log any missing fields and exit if the file is incomplete.

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

> **Warning:** changing `model.embedding_model` requires a migration — engram will refuse to start and tell you to run `engram migrate`.

## Switching embedding models

If you change `model.embedding_model` in your config, engram will detect the mismatch on startup and return an error:

```
engram: embedding model changed from "KnightsAnalytics/all-MiniLM-L6-v2" to "your/new-model"
— run 'engram migrate' to re-embed all memories
```

Run the migration command to re-embed all memories with the new model:

```bash
engram migrate
```

Migration is atomic — the new collection is fully built before the old one is removed. If it fails partway through, your existing memories are untouched.

## Tools

| Tool | Required | Optional |
|------|----------|----------|
| `store` | `content`, `type` | `tags` |
| `search` | `query` | `min_score` (default 0.5) |
| `list` | — | `type_filter`, `tag_filter`, `limit` (default 20) |
| `delete` | `memory_id` | — |
| `update` | `memory_id`, `content` | — |

**Memory types:** `preference`, `task`, `fact`, `action`

`min_score` is a cosine similarity threshold (0–1). Results below it are excluded. Omit to use the default of 0.5.

`delete` and `update` return an error if `memory_id` does not exist.

### Examples

```
store(content="prefers dark mode", type="preference", tags=["ui"])
search(query="UI preferences")
search(query="UI preferences", min_score=0.7)
list(type_filter="fact", tag_filter="kubernetes")
update(memory_id="<id>", content="updated content")
delete(memory_id="<id>")
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
