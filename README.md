# engRam

A long-term semantic memory MCP server — single statically-linked Go binary with no external services required.

**Name:** *engram* (a memory trace in neuroscience) + *RAM* — stores memories, fast.

## Features

- **Single binary** — no Python, no Docker, no runtime dependencies
- **Local embeddings** via [hugot](https://github.com/knights-analytics/hugot) + GoMLX ([jina-embeddings-v2-small-en](https://huggingface.co/jinaai/jina-embeddings-v2-small-en), 8192-token context, downloaded once on first run)
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

> Internet access is required on first run to download the embedding model from Hugging Face (~130MB, one-time only).

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

If the config file does not exist, engram creates it with defaults on first run. Every field is optional: any field you omit falls back to its default, and engram notes on stderr which defaults it used. Your config file is never rewritten, so a partial config managed by a dotfiles tool stays exactly as you wrote it. Malformed JSON, or an out-of-range value you did set, is still an error.

`model.onnx_file_path` selects which `.onnx` file to take from the model repo, as a path relative to its root. Most repos publish several variants (fp32, fp16, quantized) and the download is rejected if the choice is ambiguous, so this must name one exactly — `model.onnx` for a file at the root, `onnx/model.onnx` for the common subdirectory layout. Change it whenever you change `embedding_model`.

`max_content_chars` caps how much content a single memory may hold. It is a guardrail against pasted logs and whole documents, not a model limit: oversize content is chunked rather than rejected, so the real cost is a slow embed and a vaguer vector that retrieves *worse*. The default of 32768 is roughly one full context window of the default model. Set it higher if you genuinely store long documents; `0` is rejected, since it would reject every memory.

`default_limit` sets the cap applied when a `search` call omits `limit`. Setting it to `0` makes uncapped the default for every search. Unlike the `limit` argument, a negative `default_limit` is rejected rather than treated as `0` — use `0` to mean uncapped here.

```json
{
  "model": {
    "path": "/path/to/models",
    "embedding_model": "jinaai/jina-embeddings-v2-small-en",
    "onnx_file_path": "model.onnx"
  },
  "db": {
    "path": "/path/to/db"
  },
  "default_limit": 20,
  "max_content_chars": 32768
}
```

> **Warning:** changing `model.embedding_model` requires a re-embed — engram will refuse to start and tell you to run `engram reembed`.

## Switching embedding models

If you change `model.embedding_model` in your config, engram will detect the mismatch on startup and return an error:

```
engram: embedding model changed from "jinaai/jina-embeddings-v2-small-en" to "your/new-model"
— run 'engram reembed' to re-embed all memories
```

Run the reembed command to re-embed all memories with the new model:

```bash
engram reembed
```

Re-embedding is atomic — the new model's database is fully built before the old one is removed. If it fails partway through, your existing memories are untouched.

## CLI

```bash
engram                    # start the MCP server on stdio (default, no args)
engram version            # print version, platform and Go toolchain
engram help               # usage summary
engram export -f <file>   # write all memories to CSV
engram import -f <file>   # read memories from CSV
engram reembed            # re-embed into a newly configured model
```

`--version`/`-v` and `--help`/`-h` work as aliases. An unrecognised flag exits
with status 2 rather than starting the server.

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
| `store` | `title` (≤100 chars), `content` (≤`max_content_chars`) | `tags`, `linked_ids` |
| `search` | `query` and/or `tag_filter` (at least one) | `limit` (omit for the configured default; `0` for no cap) |
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

`limit` caps how many ranked results come back. Omitting it applies the
configured `default_limit`; passing `0` removes the cap entirely and returns
every match (a negative value does the same). Note that this is the opposite
of the usual convention — **omitting `limit` is the capped option, `0` is the
uncapped one.**

Because weak matches are dropped by the relevance cutoff *before* `limit` is
applied, a search often returns fewer results than the limit allows. That is
the expected outcome for a narrow query, not a sign the limit was set too low;
raising it will not surface the dropped matches.

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

### Releasing

Releases are cut from `main` only. Pushing a `v*` tag triggers
`.github/workflows/release.yml`, which verifies the tagged commit is an
ancestor of `origin/main` before running GoReleaser — a tag on a feature
branch fails the workflow instead of publishing a release and updating the
Homebrew tap. GoReleaser itself has no concept of branches, so this check
lives in the workflow rather than in `.goreleaser.yaml`.

```bash
git checkout main && git pull
git tag -a v2.0.0 -m v2.0.0
git push origin v2.0.0
```

If you tag the wrong commit, delete the tag before retagging:

```bash
git push --delete origin v2.0.0
```

The version reported by `engram version` comes from the tag: GoReleaser
injects it with `-X main.version={{ .Version }}`. The constant in
`cmd/engram/version.go` is only what local source builds report, and should
name the next intended release.

## License

MIT — see [LICENSE](LICENSE).
