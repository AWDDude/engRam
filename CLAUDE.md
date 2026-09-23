# engRam

MCP server for long-term semantic memory. Single statically-linked Go binary.

## Stack

- **Storage**: bbolt (embedded, single file, ACID transactions) — records, vectors, and links in one database
- **Retrieval**: hybrid — brute-force cosine over stored vectors + in-memory BM25, fused by Reciprocal Rank Fusion
- **Embeddings**: hugot + GoMLX simplego backend (`jinaai/jina-embeddings-v2-small-en`, 512-dim, 8192-token window, downloaded once from Hugging Face, no external service)
- **MCP transport**: stdio (mark3labs/mcp-go), proxied to a shared daemon over a unix socket
- **Concurrency**: many client processes, one daemon that owns the database

## Commands

```bash
make build    # CGO_ENABLED=0 static binary → ./engram
make test     # go test -v -race ./...
make clean    # remove binary
```

## CLI

`engram` with no args connects to the daemon and pipes MCP over stdio.
Subcommands: `version` (also `--version`/`-v`), `help` (also `--help`/`-h`),
`export -f`, `import -f`, `reembed`, `daemon` (plus `daemon status` and
`daemon stop`). An unrecognised flag exits 2 instead of silently starting the
server. `commands` in `version.go` must list every subcommand `main` dispatches
on; a test checks it against the usage text.

Version lives in `cmd/engram/version.go` as a `var`, overridden via `-X
main.version=...` ldflags: goreleaser passes the pushed tag for published
binaries, and `make build` passes the short git commit for local builds. A
bare `go build ./cmd/engram/` reports the fallback `"dev"`.

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
    ├── jinaai_jina-embeddings-v2-small-en.db    # bolt file, one per model
    ├── engram.sock                              # daemon socket (0600)
    ├── daemon.lock                              # ownership: held for the daemon's life
    ├── spawn.lock                               # client-side spawn debounce
    ├── daemon.pid
    └── daemon.log
```

The daemon's runtime files sit in the database directory rather than a global
location, so a config with a different `db.path` gets its own daemon instead of
contending for one socket.

The bolt file holds two buckets: `memories` (JSON records) and `vectors`
(binary float32 blobs, one per content chunk). One file per model is what lets
`reembed` build the new model's database before deleting the old.

## Architecture

```
cmd/engram/          # binary entry point
  main.go            # no args: config → daemon.Proxy (a byte pipe)
  daemon.go          # `daemon`, `daemon stop`, `daemon status`

internal/config/     # Config struct + Load/Default
internal/daemon/     # socket paths, client dial/spawn, daemon serve loop
internal/store/      # Store interface, boltStore, BM25 index, hugot embedding
internal/server/     # App + MCP handlers + RegisterTools
internal/mcptest/    # minimal MCP client, used only by tests
```

### Why there is a daemon

bbolt locks its file exclusively for the lifetime of a process, and `boltStore`
serves every read from in-memory maps loaded once at open. So one process per
MCP session could neither share the database nor see another session's writes:
a second session died with `another engram process still has it open`.

Rather than invent a cross-process locking protocol, one process keeps owning
the database exactly as before and the others became clients of it. Everything
that makes the store correct — the `sync.RWMutex`, one bolt transaction per
logical operation — still runs in a single process, so none of it had to be
re-argued. The daemon also loads the embedding model once instead of per
session.

- **The client is a byte pipe.** `daemon.Proxy` copies bytes both ways and
  parses nothing. The wire is newline-delimited JSON-RPC in both directions, so
  there is no second protocol to version or keep in step with the tools.
- **The daemon drives `MCPServer.HandleMessage` itself** (`session.go`) rather
  than mcp-go's `StdioServer.Listen`. `Listen` registers a package-level
  singleton session with the fixed id `stdio`, so a second concurrent
  connection fails to register and the two fight over one writer. Each
  connection gets its own `socketSession` instead.
- **Two locks, deliberately.** `daemon.lock` is ownership, held for the
  daemon's whole life, and is what guarantees a single owner. `spawn.lock` only
  debounces a burst of clients each starting a daemon. They must stay separate
  files: a client holds the spawn lock while the daemon it just started is
  booting, and if they were one lock that daemon would find its own parent
  holding it and exit immediately.
- **`stop` waits for the ownership lock, not just the socket.** A closed
  listener does not mean the process is gone, and every caller of `stop` starts
  a replacement right afterwards.
- **Shutdown closes live connections.** Closing the listener does not unblock a
  session already reading from its client, so `accept` cancels the
  per-connection context before waiting, or a `daemon stop` would hang until
  every session happened to leave.
- **Idle shutdown** after `DefaultIdleTimeout` with nothing attached. An idle
  session still holds its connection, so this only fires once every session is
  gone.
- **Version skew:** the daemon writes `engram-daemon <version>` before the MCP
  stream, and a client that reads a different version retires the daemon and
  starts its own. Without it a `brew upgrade` would keep serving old code until
  the machine rebooted.

CLI commands that need the bolt file (`export`, `import`, `reembed`) call
`daemon.StopIfRunning` and then open it directly. That is only reasonable
because spawning is automatic, and it is what keeps them simple
direct-to-bolt commands instead of needing a quiesce protocol or a set of MCP
tools that would also put `reembed` in front of the model.

- **`Store` interface** (`internal/store/store.go`) — all persistence behind one interface, fully mockable
- **`boltStore`** (`internal/store/bolt.go`) — production store. Each logical operation lands in one bolt transaction, so the bidirectional-link invariant holds by construction. In-memory maps serve reads and are updated only after a commit succeeds, so a failed write can't desync them.
  - `Add` and `Update` each build every change — the record, its tags and text, and the peer records a link patch moves — and issue **one** `commit`. Both used to take two transactions (the record, then `syncLinks`), which let another session observe one without the other, and left `Add` creating an unlinked record when a linked id turned out not to exist. `syncLinksLocked` returns changes rather than committing them, and takes the target by value so `Add` can link a record that is not in `s.docs` yet.
  - Consequently a failed `store` creates nothing, and `Add` returns an empty id on error. The handler used to surface the id of the half-created memory; there is no longer one to surface.
  - Link validation collects **every** missing id into a `MissingLinksError` rather than failing on the first, so one corrected retry can fix them all instead of one per attempt.
  - `writeFailure` (`internal/server/handlers.go`) is what the caller reads, and the caller is a language model. It states outright that nothing was written, which is only safe to claim because every `Store` write is atomic (the interface doc says so). Without it a model cannot tell a rejected call from a half-applied one and has to guess whether retrying is safe.
  - Embedding covers title and content together and has to run outside the lock, so `tryUpdate` re-checks under the lock that the text it embedded from is still current, and retries if not. Otherwise a content-only patch writes back the title as it looked before a concurrent title change.
  - `Retrieve` assembles a record and its linked summaries under one `RLock`. The handler used to walk the links with a `GetByID` each, which a concurrent write could land between.
- **`bm25Index`** (`internal/store/bm25.go`) — lexical index over title/tags/content, rebuilt at open and never persisted. Also holds the RRF fusion and the relative-cutoff helpers.
- **`App`** + **handlers** (`internal/server/`) — one method per MCP tool, uses `BindArguments` for typed arg parsing
- **`RegisterTools`** (`internal/server/tools.go`) — declarative tool schema registration

## Tools exposed

| Tool | Required args | Optional args |
|------|--------------|---------------|
| `store` | title (≤100 chars), content | tags, linked_ids |
| `search` | — | query, tag_filter (array), limit (omit = configured default; 0 = no cap), offset |
| `list_tags` | — | — |
| `retrieve` | memory_id | — |
| `delete` | memory_id | — |
| `update` | memory_id, plus at least one of: title, content, tags, add_tags, remove_tags, linked_ids, add_linked_ids, remove_linked_ids | — |

Every tool is served by the shared daemon, so two sessions calling them at the
same time is the normal case rather than an edge one. Requests from a single
client are handled in order; different clients run concurrently, and the
store's read lock lets their searches overlap.

`search` is hybrid: dense vector similarity and lexical BM25 fused by Reciprocal Rank Fusion, with titles and tags weighted above body text. Results are ranked and cut off relative to the best match, so there is no similarity threshold to configure. Omitting both `query` and `tag_filter` lists every memory instead, newest first, with no relevance filtering — the way to enumerate the store (audits, dedup checks, verifying a bulk operation touched everything).

Tags are normalized to lowercase on write (`store`, `update`) regardless of the case given, and normalized again on every read path (`search`, `retrieve`, `list_tags`) so tags written before this rule existed still come back lowercased without a data migration. `tag_filter` takes one or more tags and matches by exact equality (not substring) — a memory must carry every listed tag (AND semantics) to match. `list_tags` returns every distinct tag in use, sorted, so a caller can discover valid `tag_filter` values instead of guessing.

`update`'s `title` and `content` are each independently optional — omit either to leave it unchanged. Tags and links can each be changed two ways: `tags`/`linked_ids` replaces the whole set, or `add_tags`/`remove_tags` and `add_linked_ids`/`remove_linked_ids` change it incrementally (union, then subtract, so an entry in both ends up removed) without touching entries not mentioned. `tags` is rejected if combined with `add_tags`/`remove_tags`, and likewise `linked_ids` with `add_linked_ids`/`remove_linked_ids` — each pair is mutually exclusive ways of expressing the same patch. The additive/subtractive link forms exist specifically so that adding one link doesn't require first reading and replaying the whole existing set (previously the only way, and a silent-data-loss risk if a caller forgot to).

`limit` inverts the usual convention: **omitting it caps results** at `default_limit`, while **passing 0 removes the cap** (negative behaves as 0). The relevance cutoff runs *before* `limit`, so fewer results than the limit is the normal outcome for a narrow query — raising the limit will not surface the dropped matches. In config, `default_limit: 0` makes uncapped the default, but a negative `default_limit` is rejected rather than treated as 0. `offset` skips that many matches before `limit` is applied, for paging. The response is `{results: [{id, title, tags, created_at, updated_at}, ...], total}`, where `total` is the match count before `offset`/`limit` were applied (post-relevance-cutoff for a ranked query, or the full matching count when listing everything) — a caller knows it has seen everything once `offset + len(results) >= total`. Use `retrieve` for full details (including linked memories' id/title/tags/created_at/updated_at). `linked_ids` are bidirectional — linking or unlinking a memory automatically updates the memories on the other end, and deleting a memory cascades the cleanup. There is no `type` field; tags are the only categorization mechanism.

`updated_at` is set equal to `created_at` when a memory is stored, and refreshed on every `update` — including a tags-only or links-only patch, and on the other end of a bidirectional link change, since that also changes a stored field on that record. Memories written before `updated_at` existed come back with it defaulted to `created_at` rather than empty, the same self-healing-on-read pattern tags use.
