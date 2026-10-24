# logmetrics

**logmetrics** is an embedded Go library (package `metrics`) for writing,
indexing, selecting and retaining time series in a local directory.

It contains a **focused source fork** of the
[VictoriaMetrics](https://github.com/VictoriaMetrics/VictoriaMetrics) storage
engine, not an HTTP wrapper. The necessary storage, compression, indexing,
and support packages live in this repository under `internal/`. The package
supplies its own small Go-only lifecycle, configuration, ingestion and
selection API. There is no executable, collector, network listener, YAML
file or command-line configuration.

**Independent engine source:** No Go module imports or dependencies on
`github.com/VictoriaMetrics/VictoriaMetrics`, `github.com/VictoriaMetrics/metrics`,
`github.com/VictoriaMetrics/metricsql`, or sibling VictoriaMetrics modules.
Everything besides `golang.org/x/sys` is the Go standard library or a
source-level fork under `internal/`. See [FORK_NOTICES.md](FORK_NOTICES.md)
for upstream attribution and the preserved license texts.

## Quick start

Go 1.26.10 or newer is required by the forked engine source.

```bash
go get github.com/marcgauthier/metrics
```

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/marcgauthier/metrics"
)

func main() {
    db, err := metrics.Open("./data",
        metrics.WithRetentionDays(30),
        metrics.WithFlushInterval(2*time.Second),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    err = db.Write(metrics.Sample{
        Metric: "http_requests_total",
        Labels: map[string]string{
            "job":      "gateway",
            "instance": "edge-01",
        },
        Value: 1024,
    })
    if err != nil {
        log.Fatal(err)
    }

    // Ingested data becomes selectable after the normal flush cycle.
    time.Sleep(3 * time.Second)

    series, err := db.Select(context.Background(), metrics.Selector{
        Metric: "http_requests_total",
        Labels: map[string]string{"job": "gateway"},
    }, metrics.TimeRange{}, 100)
    if err != nil {
        log.Fatal(err)
    }
    for _, s := range series {
        for _, p := range s.Points {
            fmt.Println(s.Metric, s.Labels, p.Time, p.Value)
        }
    }
}
```

## Encrypted storage (AES-256-GCM)

Encryption is optional for a new database. When enabled, compression
occurs **first**, and authenticated AES-256-GCM encryption occurs in the
forked file-I/O layer **immediately before data and index bytes are written to
disk**. Reading does the inverse: authenticate and decrypt the necessary
64-KiB ciphertext chunks, then let the original storage engine
decompress and search the logical bytes. No unencrypted temporary copies of
the storage data are written by this hook.

```go
key := make([]byte, 32) // Load from your secure secret manager; NOT a hard-coded value.
db, err := metrics.Unlock("./data", key,
    metrics.WithRetentionDays(30))
if err != nil { return err }
defer db.Close()
```

Equivalent call: `metrics.Open("./data", metrics.WithEncryptionKey(key))`.
The key must contain **exactly 32 raw bytes**. A keyed database cannot be
opened without the key, and an incorrect key is rejected *before* the storage
engine opens. The key itself is never saved to disk. The `.metrics-key`
marker contains only an encrypted known-value verifier and a random nonce.

Every storage file except the `.metrics-key` marker and the empty
`flock.lock` coordination file carries the `MTXAEAD1` envelope: compressed
`.bin` data and index blocks, `.json` metadata, and the small metric-ID and
tracker files. Filesystem paths, folder names and partition dates remain
visible. Each file has an authenticated header and length trailer; corruption
and truncation fail authentication. Files are encrypted in chunks to preserve
random-access search without a temporary decrypted database.

Encrypted databases skip on-disk persistence of the TSID/metric-name caches
(the fastcache file format cannot carry the envelope); the advisory caches
rebuild in memory with identical query results.

**Important:** enabling encryption on an existing plaintext database is
deliberately blocked; there is no automatic in-place migration or key rotation
yet. Take a verified backup and perform an explicit migration using a new
database. Decryption is supported only while the database is unlocked; losing
the key means losing access to its stored metrics.

## Go API

| Operation | Function |
| --- | --- |
| Open/create local storage | `Open(dir, options...)` |
| Open encrypted storage | `Unlock(dir, key, options...)` |
| Insert a sample | `db.Write(Sample)` |
| Batch insert | `db.WriteBatch([]Sample)` |
| Select time series | `db.Select(ctx, Selector, TimeRange, maxSeries)` |
| Stream selected series | `db.Query(ctx, Selector, TimeRange, callback)` |
| Latest point per series | `db.Latest(ctx, Selector, maxSeries)` |
| Read storage stats | `db.Stats()` |
| Force search visibility (debug/test) | `db.Flush()` |
| Close and release directory lock | `db.Close()` |

Options (all Go functions): `WithRetentionDays`, `WithFlushInterval`,
`WithMinFreeDiskBytes`, and `WithEncryptionKey`.

The default retention is **7 days**, default flush interval is **2 seconds**.
A single process can open a given directory at a time.

### More selection examples

```go
// Every series, capped at 1000 (fails with ErrTooManySeries past the cap)
series, err := db.Select(ctx, metrics.Selector{}, metrics.TimeRange{}, 1000)

// One metric over the last hour
series, err = db.Select(ctx,
    metrics.Selector{Metric: "cpu_temperature", Labels: map[string]string{"host": "edge-01"}},
    metrics.TimeRange{Start: time.Now().Add(-time.Hour)},
    100)

// Current value of every http_requests_total series
latest, err := db.Latest(ctx, metrics.Selector{Metric: "http_requests_total"}, 1000)

// Read without materializing all the series in memory at once
err = db.Query(ctx, metrics.Selector{Metric: "cpu_temperature"}, metrics.TimeRange{},
    func(s metrics.Series) error {
        fmt.Println(s.Labels, s.Points)
        return nil
    })
```

Series are returned ordered by metric name and labels; points within a
series are in ascending time order. `Select` fails with `ErrTooManySeries`
instead of truncating when more than `maxSeries` series match; the cap
counts series indexed on the days covered by the time range.

### Batch writes

```go
err := db.WriteBatch([]metrics.Sample{
    {Metric: "jobs_completed_total", Labels: map[string]string{"queue": "mail"}, Value: 11},
    {Metric: "jobs_completed_total", Labels: map[string]string{"queue": "push"}, Value: 4},
})
```

Batch sizes are bounded internally. Concurrent writers and readers are
supported. Metric and label names may not be empty or start with `__`;
value must be finite. No single name or value may exceed 65535 bytes, and
the metric name plus each label (name and value) may not exceed 65472
bytes combined, matching what the engine index can store; oversized
samples are rejected instead of silently dropped. Set timestamps via
`Sample.Time` (otherwise the current time is used); sub-millisecond
precision is truncated to milliseconds. Writing the same series and
millisecond twice keeps the last value.

## Storage, indexing, compression, retention

- **Storage:** VictoriaMetrics manages compressed time-series blocks in a directory, including indexes, caches and background maintenance.
- **Search:** series selection by exact metric name and label matchers over the storage-layer index.
- **Retention:** `WithRetentionDays(N)` uses the engine's background data removal. Cleanup occurs periodically, not at an exact second. Disk-space limits are not supported by the engine; use retention plus `WithMinFreeDiskBytes` as the safeguard.
- **Backfilling:** samples older than the configured retention are rejected before ingestion; timestamps more than 24 hours ahead are also rejected.
- **Visibility:** newly written samples are not necessarily selectable immediately. The engine buffers them; `Flush()` forces visibility but is expensive and should not be used for every write.
- **Durability:** use `Close()` for an orderly shutdown. The flush interval is not a guarantee that every recent write survives abrupt power loss.
- **Locking:** the storage engine enforces an exclusive process-level filesystem lock for the data directory.
- **Resource safeguards:** `WithMinFreeDiskBytes` causes the engine to become read-only when free space is insufficient. Flush interval and free-space settings are process-wide: the last DB opened wins.
- **Errors:** ingestion APIs reject invalid parameters and timestamps; the engine may still drop records that exceed its internal limits (see `Stats`). The engine exposes `Must*` operations that can panic on unrecoverable storage failures.

## Architecture and upstream reuse

```text
Your Go application
        |
  github.com/marcgauthier/metrics (package metrics)
    - options.go   Go configuration and validation
    - metrics.go   lifecycle, writes, selection, stats
        |
  github.com/VictoriaMetrics/VictoriaMetrics/lib/storage
    - MetricRow ingestion and validation
    - per-day/global series index
    - compressed time-series blocks
    - partition retention and merging
        |
    local directory
```

**Reuse strategy:** the package embeds a focused source fork of the upstream
`lib/storage` Go package and the packages it needs. It neither forks nor
patches the VictoriaMetrics binary, and never invokes its HTTP service. The
fork keeps the critical storage/index/compression implementation rather than
reimplementing it, while removing everything the embedded API does not need:
no server, no flags, no CGO (the pure-Go zstd backend is always used).

The engine dependency is forked from
`github.com/VictoriaMetrics/VictoriaMetrics v1.150.0`. Its upstream code is
licensed under [Apache License 2.0](https://github.com/VictoriaMetrics/VictoriaMetrics/blob/v1.150.0/LICENSE);
upstream notices and license terms continue to apply.

## Verification

```bash
go mod tidy
go test -race ./...
go vet ./...
```

The GitHub Actions workflow performs these checks on Linux. The tests cover
writes, batches, selection, streaming, latest-point reads, series caps,
reopening persisted metrics, concurrent writers, invalid timestamps and
names, encrypted roundtrips with a full on-disk envelope inventory, and
idempotent shutdown. `tests-live/` adds black-box applications that
exercise only the public API; see
[tests-live/README.md](tests-live/README.md).

## Non-goals

- No VictoriaMetrics server process, HTTP API, cluster coordination, remote ingestion or UI.
- No MetricsQL/PromQL expression evaluation: selection is by exact metric name and label matchers. Query issues against this library accordingly.
- No bespoke time-series compression/index algorithm: those are intentionally reused from VictoriaMetrics.
- No disk-usage-cap eviction: the engine does not support it; retention plus read-only-on-low-disk is the safeguard.

This is an initial library implementation, not a claim of production readiness.
