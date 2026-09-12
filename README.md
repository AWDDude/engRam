# engRam

A long-term semantic memory MCP server — single statically-linked Go binary with no external services required.

**Name:** *engram* (a memory trace in neuroscience) + *RAM* — stores memories, fast.

## Features

- **Single binary** — no Python, no Docker, no runtime dependencies
- **Local embeddings** via [hugot](https://github.com/knights-analytics/hugot) + GoMLX ([all-MiniLM-L6-v2](https://huggingface.co/KnightsAnalytics/all-MiniLM-L6-v2), Apache 2.0, downloaded once on first run)
- **Single-file storage** via [bbolt](https://github.com/etcd-io/bbolt) — records, vectors, and links in one ACID database
- **5 MCP tools** — store, search, retrieve, update, delete
- **Hybrid search** — vector similarity and BM25 keyword matching fused by Reciprocal Rank Fusion, so exact tokens and fuzzy recall both work
- **Bidirectional links** between memories, with cascading cleanup on delete
- **XDG-compliant** data and config paths on all platforms
- **Model migration** — switch embedding models without losing memories
- **CSV export/import** — back up and restore all memories

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
  },
  "default_limit": 20
}
```

> **Warning:** changing `model.embedding_model` requires a re-embed — engram will refuse to start and tell you to run `engram reembed`.

## Switching embedding models

If you change `model.embedding_model` in your config, engram will detect the mismatch on startup and return an error:

```
engram: embedding model changed from "KnightsAnalytics/all-MiniLM-L6-v2" to "your/new-model"
— run 'engram reembed' to re-embed all memories
```

Run the reembed command to re-embed all memories with the new model:

```bash
engram reembed
```

Re-embedding is atomic — the new model's database is fully built before the old one is removed. If it fails partway through, your existing memories are untouched.

## Export / import

Back up all memories to a CSV file, or restore them from one:

```bash
engram export -f memories.csv
engram import -f memories.csv
```

The CSV has columns `id, title, content, tags, linked_ids, created_at` (tags and linked_ids joined with `;`). Import preserves IDs, tags, links, and creation timestamps from the file; rows with a blank ID are assigned a new one.

## Tools

| Tool | Required | Optional |
|------|----------|----------|
| `store` | `title` (≤100 chars), `content` | `tags`, `linked_ids` |
| `search` | `query` and/or `tag_filter` (at least one) | `limit` (default from config; 0/negative = unlimited) |
| `retrieve` | `memory_id` | — |
| `delete` | `memory_id` | — |
| `update` | `memory_id`, plus at least one of `title`, `content`, `tags`, `linked_ids` | — |

There is no `type` field — tags are the only categorization mechanism.

`search` runs a hybrid query: dense vector similarity and lexical BM25 in
parallel, fused by Reciprocal Rank Fusion. The lexical half is what finds a
half-remembered exact token (an identifier, an error string, a name) that
embeddings alone tend to smooth over; titles and tags are weighted above body
text. Results are ranked by relevance and cut off relative to the best match,
so there is no similarity threshold to configure.

`search` returns only `{id, title, tags}` per match — use `retrieve` for full
details, including the id/title/tags of linked memories.

`linked_ids` are bidirectional: linking or unlinking a memory updates the
memory on the other end too, and deleting one cascades the cleanup.

`delete`, `update`, and `retrieve` return an error if `memory_id` does not exist.

### Examples

```
store(title="Editor preference", content="prefers dark mode", tags=["ui"])
search(query="UI preferences")
search(query="UI preferences", limit=5)
search(tag_filter="kubernetes")
retrieve(memory_id="<id>")
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
