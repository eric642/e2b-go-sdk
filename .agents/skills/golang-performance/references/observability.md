# Production Observability and Investigation Sessions

Production monitoring tools complement local profiling (`pprof`, benchmarks) by providing time-series runtime metrics, temporary high-resolution investigation sessions, continuous profiling, and regression detection across deploys.

## Table of Contents

- [Temporary Investigation Session Setup](#temporary-investigation-session-setup)
- [Prometheus Go Runtime Collector](#prometheus-go-runtime-collector)
  - [PromQL Queries for Performance Diagnosis](#promql-queries-for-performance-diagnosis)
  - [Alerting Rules](#alerting-rules)
  - [Host-Level Correlation](#host-level-correlation)
- [Continuous Profiling](#continuous-profiling)
  - [Pyroscope Push Mode](#pyroscope-push-mode)
  - [Pyroscope Pull Mode (via Grafana Alloy)](#pyroscope-pull-mode-via-grafana-alloy)
- [Real-Time Visualization (Development)](#real-time-visualization-development)
- [Cost Warnings](#cost-warnings)

## Temporary Investigation Session Setup

For **temporary deep-dive production investigations** (hours or days while debugging a specific issue, then disabled afterward), configure high-resolution capture on the target instance only:

1. **Reduce Prometheus scrape interval** to `<=10s` on the target instance (normally 15–30s). Shorter windows expose bursty GC or goroutine patterns that 30s intervals smooth out. Revert after investigation.

2. **Enable pprof** via environment variable — no recompile needed:

   ```bash
   kubectl set env deployment/my-service PPROF_ENABLED=true
   kubectl rollout restart deployment/my-service
   ```

3. **Enable continuous profiling** on the target instance only — not fleet-wide. Pyroscope/Parca on a single instance is manageable; enabling it on 50 replicas overwhelms the backend:

   ```bash
   kubectl set env deployment/my-service PYROSCOPE_ENABLED=true
   kubectl rollout restart deployment/my-service
   ```

4. **Enable debug logging** via env var only on the target instance — debug logging has significant throughput impact:

   ```bash
   kubectl set env deployment/my-service LOG_LEVEL=debug
   kubectl rollout restart deployment/my-service
   ```

**Key principle:** All costly debug features (pprof HTTP, continuous profiling, debug log level, trace collection) SHOULD be toggleable via environment variables for instant on/off without recompile.

## Prometheus Go Runtime Collector

**Setup:** `github.com/prometheus/client_golang` exposes `/metrics` with `promhttp.Handler()`. Default collectors automatically export Go runtime metrics (`go_goroutines`, `go_memstats_*`, `go_gc_duration_seconds`, `process_cpu_seconds_total`, etc.). For the exhaustive catalog of default and optional metrics, see [Prometheus Go Metrics Reference](./prometheus-go-metrics.md).

**Performance note:** `go_memstats_*` metrics internally call `runtime.ReadMemStats()`, which triggers a short stop-the-world pause. In Go 1.17+, prefer the `runtime/metrics`-based collector (`collectors.NewGoCollector()`) on high-throughput services:

```go
import "github.com/prometheus/client_golang/prometheus/collectors"

// Use runtime/metrics-based collector (lower overhead)
reg := prometheus.NewRegistry()
reg.MustRegister(collectors.NewGoCollector(
    collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll),
))
reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
```

### PromQL Queries for Performance Diagnosis

#### GC pressure

| PromQL | What to look for |
| --- | --- |
| `rate(go_gc_duration_seconds_count[5m])` | GC cycles/s — >2/s sustained suggests excessive allocation rate |
| `rate(go_gc_duration_seconds_sum[5m]) / rate(go_gc_duration_seconds_count[5m])` | Average GC pause — increasing trend means heap is growing or has too many pointers |
| `go_gc_duration_seconds{quantile="1"}` | Worst-case GC pause — spikes here cause tail latency (p99) |

#### Memory leaks

| PromQL | What to look for |
| --- | --- |
| `go_memstats_alloc_bytes` | Should be roughly stable under constant load; continuous increase = memory leak |
| `rate(go_memstats_alloc_bytes_total[5m])` | Allocation rate (bytes/s) — drives GC frequency; compare before/after deploy for regressions |
| `process_resident_memory_bytes - go_memstats_sys_bytes` | Gap = non-Go memory (cgo, mmap); growing gap = non-Go leak |

#### Goroutine leaks

| PromQL | What to look for |
| --- | --- |
| `go_goroutines` | Should correlate with load; growing independently of traffic = leak |
| `delta(go_goroutines[1h])` | Net goroutine change over 1h; positive without load increase = leak |

#### CPU saturation

| PromQL | What to look for |
| --- | --- |
| `rate(process_cpu_seconds_total[5m])` | CPU cores consumed; compare to `GOMAXPROCS` to detect saturation |
| `rate(process_cpu_seconds_total[5m]) / <GOMAXPROCS>` | CPU utilization ratio; >0.8 sustained = CPU-saturated |

#### Post-deploy regression detection

| PromQL | What to look for |
| --- | --- |
| `rate(go_memstats_alloc_bytes_total[5m])` | Compare before/after deploy; significant increase = new allocation pattern introduced |
| `histogram_quantile(0.99, rate(http_request_duration_seconds_bucket[5m]))` | p99 latency increase after deploy = regression (requires app-level histogram) |

### Alerting Rules

See [Example Prometheus alerting rules](../assets/prometheus-alerts.yml) (`HighGCPauseTime`, `GoroutineLeak`, `MemoryNearLimit`). Adjust thresholds to your application — a high-throughput data pipeline has different baselines than a lightweight API server.

### Host-Level Correlation

Go runtime metrics alone do not distinguish app bottlenecks from infrastructure contention:

- **`node_exporter`** — host CPU, memory, disk I/O, network. High `node_cpu_seconds_total` with low `process_cpu_seconds_total` signals a noisy neighbor rather than your Go process.
- **`process-exporter`** — per-process Linux metrics when multiple Go services share a host.

## Continuous Profiling

Continuous profiling collects low-overhead samples in production and stores them for historical comparison. Use it to detect regressions across deploys, compare flamegraphs over time, and feed PGO (see [Runtime Tuning](./runtime.md#profile-guided-optimization-pgo)).

| Tool | Model | Overhead | Best for |
| --- | --- | --- | --- |
| **Grafana Pyroscope** | push SDK or pull (via Alloy) | ~2-5% | Grafana ecosystem, historical flamegraph comparison |
| **Parca** (Polar Signals) | eBPF-based pull | <1% | Infrastructure-wide profiling, no code changes |
| **Datadog Continuous Profiler** | push (agent) | ~1-2% | Existing Datadog users |
| **Google Cloud Profiler** | push (agent) | ~1-2% | GCP-hosted Go services |

### Pyroscope Push Mode

```go
import "github.com/grafana/pyroscope-go"

pyroscope.Start(pyroscope.Config{
    ApplicationName: "myapp",
    ServerAddress:   "http://pyroscope:4040",
    ProfileTypes: []pyroscope.ProfileType{
        pyroscope.ProfileCPU,
        pyroscope.ProfileAllocObjects,
        pyroscope.ProfileAllocSpace,
        pyroscope.ProfileInuseObjects,
        pyroscope.ProfileInuseSpace,
        pyroscope.ProfileGoroutines,
    },
})
```

### Pyroscope Pull Mode (via Grafana Alloy)

No code changes required — Alloy scrapes `/debug/pprof/*` endpoints periodically. Configure Alloy to target your service's authenticated pprof endpoint.

When using third-party profiling libraries, refer to the library's official documentation for current API signatures.

## Real-Time Visualization (Development)

| Tool | What it does |
| --- | --- |
| **statsviz** (`github.com/arl/statsviz`) | Real-time browser dashboard at `/debug/statsviz` — heap, GC pauses, goroutines, scheduler. Register with `statsviz.Register(mux)`. Great for local development. |
| **expvar** (stdlib `expvar`) | JSON metrics at `/debug/vars` — lightweight, no dependencies. Integrates with Netdata, Telegraf, or custom dashboards. |

## Cost Warnings

**Profiles and traces are expensive to collect.** Keep them short-term and localized:

- **pprof CPU profiling** — CPU-intensive during the capture window. Do not run 30s profiles back-to-back in production; space them out.
- **Pyroscope continuous profiling** — ~2–5% CPU overhead **per instance, always-on**. At scale (hundreds of instances), enable on a subset of instances or on-demand via environment variable.
- **Execution traces** — generate MB/s of data. Capture 5–10s max; longer traces are unwieldy and slow to analyze.
- **Debug log level** — significant throughput impact from allocation and I/O overhead. Never leave enabled permanently.
