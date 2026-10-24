# tests-live

Small real applications that exercise the `metrics` package the way an external
program would: open a database in a temp directory, write, flush, select,
read stats, close. Each app is self-checking — exit 0 plus `PASS` on success,
non-zero with a `FAIL` message on any mismatch.

The apps use only the public `metrics` API and the standard library. No
`internal/` imports, so every check proves something a real user can rely on.

## Run

```bash
go test ./tests-live/ -v        # build and run every app
go run ./tests-live/write-select # run one app directly
```

## Apps

| App | Proves |
| --- | --- |
| `write-select` | Basic flow: write with labels/timestamps, metric and label select, zero time means now, nil labels OK |
| `batch-query` | `WriteBatch` across the internal chunk boundary, streaming `Query`, series cap fails loudly |
| `time-ranges` | Full/sub/open/empty/reversed ranges, `Latest` |
| `stats-counters` | `Stats` series/rows/drops/read-only |
| `status-panel` | Renders a status panel with no unknown counter fields |
| `persist-reopen` | Data and series survive close/open; row counters reset |
| `retention-reject` | Expired/future timestamps rejected with the documented sentinels; edges land |
| `invalid-input` | Every validation error: options, names, values, batch atomicity, bad ranges, nil callback/ctx, cancelled context |
| `concurrent-writers` | Parallel writers land every point exactly once |
| `encryption-roundtrip` | `Unlock`/`Open` with key, reopen, wrong key and missing key rejected, plaintext DB unaffected, full envelope inventory |
| `close-semantics` | Idempotent `Close`, every op after close returns `ErrClosed` |
| `unicode-labels` | Emoji/CJK/accents/quotes in names and labels round-trip exactly and stay selectable |
| `empty-db` | Fresh database: zero stats, empty select/query/latest, harmless flush |
| `background-select` | Samples become selectable via the flush interval without calling `Flush` |
| `read-only` | Huge `MinFreeDiskBytes` forces read-only: `Write` fails, reads work |
| `open-options` | Every option combined, nested dir auto-created, reopen with new options |
| `limits` | 60000-byte value, exact-boundary identity and 100-label sample round-trip; past the bound rejected |
| `locked-database` | Double open and unwritable dir fail cleanly; holder unaffected |
| `crash-recovery` | Unclean shutdown: clean reopen, settled points survive, volatile points lost |
| `close-race` | Concurrent Close, Close during query, writes racing Close |
| `multi-day` | Backfilled days land, selectable per day and whole; one day crosses a month partition |
| `dedup-truncate` | Same-millisecond rewrite keeps last value; sub-ms truncated |
| `local-time` | Non-UTC timestamps round-trip as the same instant (ms precision) |
| `exact-match` | Values with regexp-like metacharacters match exactly and literally |

## Add a new app

1. Create `tests-live/<name>/main.go` (`package main`, stdlib + `metrics` only).
2. Follow the skeleton: `run() error`, print `PASS: <name>` on success, `FAIL: ...` to stderr and exit 1 otherwise. Use your own temp dir and `defer` cleanup.
3. Append `<name>` to the `apps` list in `live_test.go`.
4. Add a row to the table above.

## Notes on loggy parity

This suite mirrors `../loggy/tests-live` app for app, adapted to the
time-series API (`Select`/`Query`/`Latest` instead of LogsQL search):

- `query-pipes` has no equivalent (no query language): `time-ranges`
  covers the selection dimensions instead.
- `stats-sizes` becomes `stats-counters`: the engine exposes series and
  row counters rather than byte sizes.
- `disk-eviction` is intentionally not ported: the VictoriaMetrics
  storage engine offers no disk-usage-cap eviction, so the package
  offers no `WithMaxDiskBytes`. `dedup-truncate` covers the
  write-coalescing contract in its slot.
- `literal-search` becomes `exact-match`: label matchers are exact and
  literal by construction.
