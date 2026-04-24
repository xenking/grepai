# grepai-fork — xenking's patch set

Downstream patches applied on top of upstream `main` (at the time of fork:
`c4f294b feat(search): add configurable file-level deduplication (#188)`),
to eliminate the Voyage AI proxy workaround and harden the background
watcher for large repos + atomic-write editors.

Binary version string:
`0.35.0-fork+voyage+batch-cfg+ready+atomic+state+activity+refs`.

## Commits on `fork/main`

| SHA       | Subject                                                                            | Upstream issue/PR |
|-----------|------------------------------------------------------------------------------------|-------------------|
| `3620b2e` | feat(embedder): add Voyage AI provider with per-provider batch config              | PR #166           |
| `fddd644` | feat(config): expose `max_batch_size` / `max_batch_tokens` embedder knobs          | Issue #92         |
| `684ce83` | fix(watch): configurable `--ready-timeout`, signal ready before initial scan        | Issue #218        |
| `38be72c` | fix(watcher): re-arm fsnotify watch on atomic-write DELETE/CREATE cycles           | Issue #225        |
| `e17555e` | fix(state): split runtime state out of config.yaml                                 | eval patch 5      |
| `9503bae` | fix(status): Last updated reflects removes + incremental events                    | eval patch 6      |
| `c992f6e` | feat(trace): surface value-passed references when direct callers are empty        | eval patch 7      |
| `ce4cbc3` | feat(trace): fire value-ref fallback when callers are self-referential only        | eval patch 7 fix  |

All eight commits build with `go build ./...` and pass `go test ./...`.

## 1. PR #166 — Voyage AI provider

- Squashed PR #166 (by @c58) into a single feat commit rather than replaying
  its merge-heavy history, and applied `gofmt -w .` to resolve the
  maintainer's CHANGES_REQUESTED feedback (alignment on `mcp/server.go:37`).
- Also carries the PR's secondary change: `mcp.NewServer*` now takes a
  `rpgEnabled` boolean so the RPG tools are only advertised when the feature
  is switched on in config.
- CHANGELOG.md conflict did not materialize because we rebased onto a fresh
  `upstream/main` where the Voyage AI entry did not yet exist.

## 2. Issue #92 — configurable batch limits

- New YAML fields `embedder.max_batch_size` and `embedder.max_batch_tokens`
  (both `omitempty`, default 0 == use per-provider fallback).
- New helpers: `config.ProviderBatchDefault(provider)` and
  `EmbedderConfig.ResolveBatchLimits()`. Per-provider defaults:
  - `voyageai`: 900 / 80000  (Voyage caps at 1000 / 120k per request)
  - all others: 2000 / 280000
- Factory threads the resolved values into the concrete embedders via new
  `WithOpenAIBatchConfig` / `WithVoyageAIBatchConfig` functional options.
  The embedders expose them through the existing
  `BatchEmbedder.BatchConfig()` contract, so `embedder.FormBatches` needs no
  change.
- Tests:
  - `config/embedder_batch_test.go` — table test over defaults and partial overrides.
  - `embedder/factory_batch_test.go` — table test that overrides + defaults
    reach the actual embedder instance.

## 3. Issue #218 — `--ready-timeout` flag and ready-before-scan

- New `grepai watch --ready-timeout` duration flag (default 30s); also
  honored by the workspace watcher, which previously hardcoded 60s.
- `watchProjectWithEventObserver` now calls `onReady()` **before**
  `runInitialScan`. Readiness now means "the daemon is live" rather than
  "initial indexing is complete". This unblocks the parent CLI on
  large monorepos where initial scan can legitimately exceed 30s.
- Tests (cli/watch_ready_timeout_test.go):
  - `TestWatchReadyTimeoutFlag` — flag registration + parse.
  - `TestWatchReadySignalFiresBeforeInitialScan` — regression guard on the
    onReady / runInitialScan ordering.

## 4. Issue #225 — atomic-write directory re-arm

- Added a small state machine to `watcher.Watcher`:
  - `rememberDir` / `isWatchedDir` / `forgetDir` track the directories we
    have successfully registered with fsnotify.
  - `scheduleRewatch(path)` arms a 500ms timer on DELETE/RENAME of a
    watched directory (`atomicDirRewatchWindow`).
  - `rearmAfterCreate(path)` walks the new inode and re-registers the
    watch recursively when a CREATE arrives within the window.
  - `Close()` cancels all pending rewatch timers.
- Tests (watcher/watcher_test.go):
  - `TestAtomicRewatch_RearmsAfterDeleteCreate` — state machine happy path.
  - `TestAtomicRewatch_ForgetsAfterWindow` — cleanup when CREATE never comes.
  - `TestHandleEventArmsAndClearsRewatch` — end-to-end dispatch through
    `handleEvent` with synthetic `fsnotify.Event` values.

## 5. Eval patch 5 — state.yaml split

- `.grepai/state.yaml` now holds mutable runtime state (`last_index_time`,
  `last_activity_time`). The user-owned `config.yaml` is never rewritten
  with runtime timestamps or auto-materialized defaults (`framework_processing`,
  `search.dedup`, provider-default `endpoint`).
- `config.Load` captures the raw file bytes plus a post-defaults snapshot;
  `config.Save` writes the raw bytes back verbatim when nothing observable
  has changed, so load+save is byte-identical for untouched configs.
- Migration: a legacy `watch.last_index_time` in `config.yaml` is moved to
  `state.yaml` on first load and stripped from the file on next save (one
  INFO log line per project).
- `cli/watch.go` replaced its two `cfg.Save(root)` heartbeats with the new
  `saveRuntimeState` helper, which goes through `state.yaml` only.
- Tests: `config/roundtrip_test.go`, `config/state_migration_test.go`,
  `config/defaults_not_materialized_test.go`.

## 6. Eval patch 6 — Last activity reflects removes

- `handleFileEvent` now bumps `state.yaml`'s `last_activity_time` on
  `EventDelete`/`EventRename` and on the `needsReindex==false` skip path,
  under the same 30s throttle as index events. `last_index_time` continues
  to mean "last successful reindex" — deletes do NOT advance it.
- `grepai status` prints `Last activity: <ts>` and adds a separate
  `Last indexed: <ts>` line only when the two timestamps differ. Label
  prefixes are preserved so downstream stdout parsers keep working.
- Legacy deployments without `state.yaml` fall back to the store's chunk-
  mtime ceiling (`store.IndexStats.LastUpdated`).
- Test: `cli/event_timestamp_test.go` (placed in cli/ because
  `handleFileEvent` lives there; spec's `indexer/event_timestamp_test.go`
  location would have required an artificial extraction).

## 7. Eval patch 7 — value-passed references

- New `trace.FindValueReferences` scanner: when `trace callers` returns
  zero direct call-sites, it re-reads every indexed file and surfaces
  bare `\bName\b` identifier occurrences under a new `References` field
  in the JSON output. Each entry carries file, line, enclosing function,
  snippet, and `kind: "value-ref"`.
- New `--include-refs` flag: default `true` for `trace callers`, `false`
  for `trace graph`. When disabled and zero callers are found, a
  human-readable hint is printed to stderr in plain-text mode only.
- `Callers` and `References` are never conflated, so any downstream
  consumer that depends on the distinction keeps its semantics.
- Tests: `trace/value_refs_test.go` (per-file scanner unit tests) and
  `trace/value_refs_temporal_test.go` (integration over the new
  `trace/fixtures/temporal/` Temporal-style fixture).

## Pre-existing issues surfaced during testing

- `cli/watch_worktree_discovery_test.go` and `git/git_test.go` fail under
  `go test` if your host `~/.gitconfig` has `commit.gpgsign = true`, because
  the tests run `git commit --allow-empty` in throwaway repos and don't
  override the config. Mitigation: run tests with
  `GIT_CONFIG_GLOBAL=/dev/null go test ./...`. Filed as a known-flaky item;
  no patch yet — this is an upstream quirk, not something our patches
  introduced.

## Known follow-ups

- Upstream PR #166 also carries substantial documentation changes in
  `docs/src/content/docs/`. These were preserved. The fork does not build
  the Astro site; if we ever do, we should rerun `docs-generate`.
- The `--ready-timeout` flag is only honored by the top-level `grepai watch`
  entry point; it is not propagated to the background child process. That's
  correct for now (the child doesn't need the timeout), but if we ever add
  nested background orchestration we should revisit.
- The atomic-write rewatch window (500ms) is a package-private constant
  (`atomicDirRewatchWindow`). If atomic writes become sluggish on some
  filesystem, expose it as a `WatchConfig` field.
- PR #166's `rpgEnabled` field broadens `mcp.NewServer` / `NewServerWithWorkspace`
  signatures. Any downstream code that embeds grepai as a library will need
  to pass the extra argument. Document this when cutting a fork release.
