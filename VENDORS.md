# Vendors / third-party dependencies

This file lists the external Go modules required to build this package.
Versions are pinned in [go.mod](go.mod); refresh this file after any
`go get` / `go mod tidy` change with `go list -m all`.

Toolchain: Go 1.26.10 (see `go.mod`).

## Build dependencies

Only one external module remains. Everything else is the Go standard
library or source-level forks under `internal/` (see below).

| Module | Version | License | Used by | Purpose |
|---|---|---|---|---|
| `golang.org/x/sys` | v0.47.0 | BSD-3-Clause | forked `internal/vm/...` fs/memory/process code | Unix syscalls and CPU features |

## Forked upstream code (not downloaded)

The VictoriaMetrics storage engine is forked source-level
into `internal/` and is **not** fetched as a Go module during builds
(CI asserts no `github.com/VictoriaMetrics/` modules remain in the
graph). The same approach removed the remaining third-party libraries
whose behavior cannot be reproduced byte-identically with the standard
library (persisted hashes, compression formats, order-preserving JSON).
Provenance and retained upstream licenses are documented in
[FORK_NOTICES.md](FORK_NOTICES.md):

- `github.com/VictoriaMetrics/VictoriaMetrics` -> `internal/vm/`
- `github.com/VictoriaMetrics/easyproto` -> `internal/vm-easyproto/`
- `github.com/VictoriaMetrics/fastcache` -> `internal/vm-fastcache/`
- `github.com/VictoriaMetrics/metrics` -> `internal/vm-metrics/`
- `github.com/VictoriaMetrics/metricsql` -> `internal/vm-metricsql/`
- `github.com/valyala/histogram` v1.2.0 -> `internal/histogram/` (MIT; fastrand use replaced with `math/rand/v2`)
- `github.com/cespare/xxhash/v2` v2.3.0 -> `internal/xxhash/` (MIT; plus a vet fix for an amd64/arm64 asm prototype name mismatch present upstream)
- `github.com/valyala/quicktemplate` v1.8.0 `AppendJSONString` -> `internal/vm/lib/stringsutil` (MIT, see `UPSTREAM_LICENSE.quicktemplate`; the only used symbol)
- `github.com/klauspost/compress` v1.18.6 (zstd subset: root, zstd, fse, huff0, internal/cpuinfo, internal/le, internal/snapref, zstd/internal/xxhash) -> `internal/compress/` (BSD-3-Clause; tests dropped; nested MIT license kept at `zstd/internal/xxhash/UPSTREAM_LICENSE`)
- `github.com/golang/snappy` v1.0.0 (root package only) -> `internal/snappy/` (BSD-3-Clause; tests, cmd and misc dropped)

The fork always uses the pure-Go zstd backend: the upstream CGO
zstd files (which need the 60MB `valyala/gozstd` static libraries) are
not copied, and the `!cgo` build tags are removed from the pure-Go
files so they are used on every platform.

Standard-library replacements (no fork needed):

- `github.com/valyala/fastrand` -> `math/rand/v2`
- `github.com/valyala/gozstd` -> `internal/compress/zstd` (klauspost fork)
- `github.com/valyala/bytebufferpool` -> dropped with `quicktemplate`
