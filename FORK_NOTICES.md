# Forked upstream code and licenses

This package contains a focused, source-level fork of the VictoriaMetrics storage
engine and the related VictoriaMetrics packages required by its
in-process Go API. Packages are copied into internal/ and import paths
are rewritten for the github.com/marcgauthier/metrics module.

Upstream source: VictoriaMetrics v1.150.0 and its pinned Go module graph.
Copyright and license headers in upstream source are retained.
The original module-level licenses are copied into each internal fork
root as UPSTREAM_LICENSE, UPSTREAM_LICENSE.md, or UPSTREAM_NOTICE.

Original module provenance:
- github.com/VictoriaMetrics/VictoriaMetrics -> internal/vm/
- github.com/VictoriaMetrics/easyproto -> internal/vm-easyproto/
- github.com/VictoriaMetrics/fastcache -> internal/vm-fastcache/
- github.com/VictoriaMetrics/metrics -> internal/vm-metrics/
- github.com/VictoriaMetrics/metricsql -> internal/vm-metricsql/

Additional focused forks of third-party libraries (same license-retention
convention; see VENDORS.md for rationale):
- github.com/valyala/histogram v1.2.0 -> internal/histogram/
- github.com/cespare/xxhash/v2 v2.3.0 -> internal/xxhash/
- github.com/valyala/quicktemplate v1.8.0 (AppendJSONString only)
  -> internal/vm/lib/stringsutil
- github.com/klauspost/compress v1.18.6 (zstd subset)
  -> internal/compress/
- github.com/golang/snappy v1.0.0 (root package only, tests and cmd dropped)
  -> internal/snappy/

VictoriaMetrics source packages are no longer downloaded during a build.
The only remaining external Go module is golang.org/x/sys.
