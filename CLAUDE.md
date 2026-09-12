# engRam

MCP server for long-term semantic memory. Single statically-linked Go binary.

## Stack

- **Storage**: bbolt (embedded, single file, ACID transactions) — records, vectors, and links in one database
- **Retrieval**: hybrid — brute-force cosine over stored vectors + in-memory BM25, fused by Reciprocal Rank Fusion
- **Embeddings**: hugot + GoMLX simplego backend (`jinaai/jina-embeddings-v2-small-en`, 512-dim, 8192-token window, downloaded once from Hugging Face, no external service)
- **MCP transport**: stdio (mark3labs/mcp-go v0.50.0)

## Commands

```bash
make build    # CGO_ENABLED=0 static binary → ./engram
make test     # go test -v -race ./...
make clean    # remove binary
```

## CLI

`engram` with no args starts the MCP server on stdio. Subcommands: `version`
(also `--version`/`-v`), `help` (also `--help`/`-h`), `export -f`, `import -f`,
`reembed`. An unrecognised flag exits 2 instead of silently starting the server.

Version lives in `cmd/engram/version.go` as a `var` that goreleaser overrides
via `-X main.version={{ .Version }}`, so published binaries report their tag
and source builds report the next intended release.

## Releasing

Releases are cut from `main` only. A `v*` tag push runs
`.github/workflows/release.yml`, which asserts the tagged commit is an ancestor
of `origin/main` before GoReleaser runs — tagging a feature branch fails the
workflow rather than publishing a release and updating the Homebrew tap.
GoReleaser has no branch concept, so the check lives in the workflow, not in
`.goreleaser.yaml`. Widen it if a maintenance branch (e.g. `v1.x`) ever needs
to ship releases.

## Config

Config file location (or override with `ENGRAM_CONFIG_PATH`):
- All platforms: `~/.config/engram/config.json`
- `XDG_CONFIG_HOME` honored on all platforms (e.g. `$XDG_CONFIG_HOME/engram/config.json`)

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

**Warning:** changing `model.embedding_model` (or `model.onnx_file_path`) invalidates the stored vectors — engram refuses to start and tells you to run `engram reembed`.

Missing config file → created with defaults on first run. Partial config → omitted fields take their default, reported on stderr; the file itself is never rewritten (it may be dotfiles-managed). Malformed JSON or an explicitly set out-of-range value → error and exit.

On first run, the embedding model (`jinaai/jina-embeddings-v2-small-en`) is downloaded from Hugging Face to `model.path`. Subsequent starts load it from disk with no network access.

## Data layout

```
~/.local/share/engram/   # all platforms ($XDG_DATA_HOME/engram if set)
├── models/
│   └── jinaai_jina-embeddings-v2-small-en/  # downloaded on first run
└── db/
    ├── db_meta.json                             # records the active embedding model
    └── jinaai_jina-embeddings-v2-small-en.db    # bolt file, one per model
```

The bolt file holds two buckets: `memories` (JSON records) and `vectors`
(binary float32 blobs, one per content chunk). One file per model is what lets
`reembed` build the new model's database before deleting the old.

## Architecture

```
cmd/engram/          # binary entry point
  main.go            # wires config → store → server

internal/config/     # Config struct + Load/Default
internal/store/      # Store interface, boltStore, BM25 index, hugot embedding
internal/server/     # App + MCP handlers + RegisterTools
```

- **`Store` interface** (`internal/store/store.go`) — all persistence behind one interface, fully mockable
- **`boltStore`** (`internal/store/bolt.go`) — production store. Each logical operation lands in one bolt transaction, so the bidirectional-link invariant holds by construction. In-memory maps serve reads and are updated only after a commit succeeds, so a failed write can't desync them.
- **`bm25Index`** (`internal/store/bm25.go`) — lexical index over title/tags/content, rebuilt at open and never persisted. Also holds the RRF fusion and the relative-cutoff helpers.
- **`App`** + **handlers** (`internal/server/`) — one method per MCP tool, uses `BindArguments` for typed arg parsing
- **`RegisterTools`** (`internal/server/tools.go`) — declarative tool schema registration

## Tools exposed

| Tool | Required args | Optional args |
|------|--------------|---------------|
| `store` | title (≤100 chars), content | tags, linked_ids |
| `search` | query and/or tag_filter (at least one) | limit (omit = configured default; 0 = no cap) |
| `retrieve` | memory_id | — |
| `delete` | memory_id | — |
| `update` | memory_id, plus at least one of: title, content, tags, linked_ids | — |

`search` is hybrid: dense vector similarity and lexical BM25 fused by Reciprocal Rank Fusion, with titles and tags weighted above body text. Results are ranked and cut off relative to the best match, so there is no similarity threshold to configure.

`limit` inverts the usual convention: **omitting it caps results** at `default_limit`, while **passing 0 removes the cap** (negative behaves as 0). The relevance cutoff runs *before* `limit`, so fewer results than the limit is the normal outcome for a narrow query — raising the limit will not surface the dropped matches. In config, `default_limit: 0` makes uncapped the default, but a negative `default_limit` is rejected rather than treated as 0. It returns only `{id, title, tags}` per match; use `retrieve` for full details (including linked memories' id/title/tags). `linked_ids` are bidirectional — linking or unlinking a memory automatically updates the memories on the other end, and deleting a memory cascades the cleanup. There is no `type` field; tags are the only categorization mechanism.
