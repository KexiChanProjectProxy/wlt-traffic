# Learnings

## 2026-04-13T18:00:00Z - eBPF/Go project setup documentation for Task 1/2

### Key authoritative sources gathered:
- https://ebpf-go.dev/guides/getting-started/ - official ebpf-go getting started guide
- https://ebpf-go.dev/guides/portable-ebpf/ - shipping portable eBPF-powered applications
- https://docs.ebpf.io/linux/program-type/BPF_PROG_TYPE_SCHED_CLS/ - TC classifier program type
- https://github.com/cilium/ebpf - main library (8K stars, pure Go, MIT licensed)
- https://github.com/cilium/ebpf/blob/main/Makefile - official build pipeline reference

### Deterministic build flow:
1. `bpf2go` compiles C to BPF bytecode and generates Go scaffolding automatically
2. Generated files: `*_bpfel.o` + `*_bpfel.go` (little-endian) and `*_bpfeb.o` + `*_bpfeb.go` (big-endian)
3. Both .go files contain `//go:embed` statements embedding the .o byte slices
4. Result: standalone Go binary deployable without .o files
5. Cross-compile with `CGO_ENABLED=0 GOARCH=arm64 go build`
6. Recommend checking in generated .o and .go files for reproducible builds
7. Official cilium/ebpf Makefile pins clang to clang-20, llvm-strip to llvm-strip-20

### bpf2go invocation pattern:
```go
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -target bpfel,bpfeb -go-package=<pkg> <ident> <src.c> -- -I<include> -g -O2 -target bpf
```

### Recommended project structure for Go+ebpf greenfield:
```
cmd/traffic-count/     # main entrypoint
internal/ebpf/         # eBPF C sources + generated bindings
internal/storage/      # SQLite layer
internal/runtime/      # loader, flush loop
internal/http/         # REST API
bpf/                   # eBPF C sources (alternative placement)
testdata/              # test fixtures
Makefile               # build targets
```

### TC sched_cls program constraints (BPF_PROG_TYPE_SCHED_CLS):
- Available since Linux 4.1
- Context: `struct __sk_buff` (network packet buffer)
- ELF sections: `tc/ingress`, `tc/egress`, `classifier`, `tc` (deprecated)
- For kernel >=6.6: use `tcx/ingress`, `tcx/egress` with BPF_LINK_TYPE_TCX
- Return codes: `TC_ACT_UNSPEC(-1)`, `TC_ACT_OK(0)`, `TC_ACT_RECLASSIFY(1)`, etc.
- For direct-action mode (accounting, not filtering): return TC_ACT_OK to let packet pass
- Context fields accessible: `data`, `data_end`, `len`, `ifindex`, `Ethernet MAC fields` (with bounds checking)
- Maps accessible: BPF_MAP_TYPE_HASH, BPF_MAP_TYPE_ARRAY, LRU variants, perf arrays, etc.
- NOT accessible: many kernel internal structures; only verifier-approved fields

### MAC extraction from skb (verifier-safe pattern):
```c
void *data = (void *)(long)skb->data;
void *data_end = (void *)(long)skb->data_end;
struct ethhdr *eth = data;
if ((void *)(eth + 1) > data_end)
    return TC_ACT_OK;
// eth->h_source = source MAC (sender)
// eth->h_dest = dest MAC (receiver)
```

### Attachment for TC (cilium/ebpf link package):
- `link.AttachTCX()` for kernel >=6.6 (preferred, uses bpf_link)
- For older kernels: use `github.com/florianl/go-tc` via netlink (cilium/ebpf has no native TC netlink attachment)
- Must create `clsact` qdisc first: `tc qdisc add dev <if> clsact`
- Program sections: `SEC("tc/ingress")`, `SEC("tc/egress")`

### ELF section naming for sched_cls:
| Attach Type | ELF Section |
|------------|-------------|
| BPF_TCX_INGRESS | `tc/ingress` or `tcx/ingress` |
| BPF_TCX_EGRESS | `tc/egress` or `tcx/egress` |
| (legacy cls_bpf) | `classifier` or `tc` |

### ebpf-go requirements:
- Go: 1.21+ (from cilium/ebpf v0.15+)
- Linux: amd64, arm64 with kernel.org LTS releases >= 4.4
- Dependencies: `golang.org/x/sys`, `golang.org/x/exp` only (pure Go, no CGO)
- RLIMIT_MEMLOCK handling needed for kernels < 5.11 (use `ebpf/rlimit.RemoveMemlock()`)

### Map declaration pattern (libbpf style):
```c
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __type(key, struct traffic_key);
    __type(value, struct traffic_counter);
    __uint(max_entries, 262144);
} traffic_map SEC(".maps");
```

## 2026-04-13T15:36:34Z - Task 2 implementation learnings
- Implemented TC-only directional accounting in `/home/kexi/traffic-count/bpf/ingress.c` and `/home/kexi/traffic-count/bpf/egress.c` with fixed rule: ingress uses destination MAC, egress uses source MAC.
- Established explicit bounded hash map definition with libbpf-style declaration (`SEC(".maps")`, `max_entries=262144`) and key/value schema suitable for batch userspace reads.
- Added Ethernet bounds checks (`eth + 1 <= data_end`) before MAC extraction to satisfy verifier-safe parsing guardrails.
- Added Task 2 Go-side schema mirrors (`TrafficKey`, `TrafficCounters`) and layout tests in `/home/kexi/traffic-count/internal/ebpf/layout_test.go` for contract verification.
- Added reproducible generation hooks in `/home/kexi/traffic-count/internal/ebpf/generate.go` using `bpf2go` directives, and Makefile target wiring via `build-ebpf` and `verify-ebpf-layout`.

## 2026-04-13T16:00:00Z - Task 4 implementation learnings
- Implemented comprehensive configuration validation with strict field validation for all config parameters including interface allowlist, bind address (loopback-only), flush interval, log levels, and database path.
- Created bootstrap validation system that performs startup validation including interface existence, Linux capabilities (root required), TC prerequisites, and explicit partial-vs-full attach mode logic.
- Established runtime contract with three states: "healthy" (all interfaces attached), "degraded" (partial attach with `allow_partial_attach=true`), and "failed" (zero successful attaches or partial attach not allowed).
- Implemented strict fail-fast behavior for invalid configurations with actionable error messages, avoiding silent configuration degradation.
- Added comprehensive test coverage for all validation pathways including edge cases like invalid MAC formats, non-loopback bind addresses, and partial attach scenarios.
- Discovered that eBPF compilation (clang dependency) doesn't block configuration validation, allowing development to continue while environment setup is resolved.

## 2026-04-13T15:45:55Z - Task 3 SQLite schema and repository implementation learnings
- Implemented SQLite schema with proper uniqueness constraints: `(interface_name, ifindex, mac)` for totals and checkpoints, `(interface_name, ifindex, mac, date_utc)` for daily buckets.
- Used SQLite ON CONFLICT clauses for atomic upsert behavior that correctly accumulates counter values while maintaining uniqueness.
- Added WAL mode via `PRAGMA journal_mode = WAL` for better concurrency and durability in concurrent write scenarios.
- Created comprehensive repository interface with methods for upsert, query, pruning, and checkpoint recovery operations.
- Implemented UTC date semantics throughout: daily storage as YYYY-MM-DD format, pruning using UTC calculations, and proper time window queries.
- Added performance optimizations with specific indexes on query patterns and test coverage for all repository primitives.
- Established transactional operations with explicit tx.Begin()/tx.Commit() patterns for data integrity.
- Created robust test suite covering schema creation, upsert durability, uniqueness constraints, pruning behavior, and UTC semantics.

## 2026-04-13T15:45:55Z - Task 5 eBPF loader and TC attachment manager implementation learnings
- Implemented complete Loader type with thread-safe attachment management using sync.RWMutex for concurrent-safe operations.
- Created AttachState enum (StateDetached/StateAttached/StateFailed) and AttachResult struct for per-interface state tracking.
- Implemented graceful degradation for environments without eBPF artifacts using EBPF_MOCK_ATTACH environment variable.
- Designed mock attachment capability that allows full lifecycle testing without kernel dependencies.
- Created idempotent cleanup operations with proper resource management and error handling.
- Established runtime service integration with status reporting methods (GetLoader(), GetLoaderStatus()).
- Maintained backward compatibility with legacy LoadEBPF() and AttachTC() functions.
- Used interface{} types instead of specific eBPF types for robustness in development environments.
- Implemented comprehensive status reporting: IsHealthy(), IsDegraded(), IsFailed(), GetAttachedInterfaces(), etc.
- Created thread-safe state management with proper locking during all operations.
- Added proper error handling for missing eBPF artifacts in development environments.
- Designed mock types (mockEBPFCollection, mockLink) for testing without kernel dependencies.

## 2026-04-13T18:30:00Z - Task 1 scaffold rebuild learnings
- Workspace had prior evidence of Task 1 completion but source files were missing from disk
- Systematic rebuild of minimal scaffold required: go.mod, Makefile, cmd/, internal/*/, bpf/, testdata/
- Build pipeline must enforce deterministic order: eBPF artifact step before Go binary step
- Placeholder eBPF C files with TODO comments acceptable for Task 1 scope
- LSP diagnostics must be clean (0 errors) before considering scaffold complete
- Comments in Go files must be minimal per ai-slop-remover rules - avoid unnecessary docstrings

## 2026-04-13T19:00:00Z - Task 2 eBPF implementation learnings
- TC ingress/egress programs implemented with proper map sharing via extern declaration
- Key: struct traffic_key { __u32 ifindex; __u8 mac[6]; } = 10 bytes
- Value: struct traffic_counter { bytes, packets, ingress_bytes, ingress_packets, egress_bytes, egress_packets } = 48 bytes
- Map: BPF_MAP_TYPE_HASH with max_entries=262144
- Fixed rule: ingress counts eth->h_dest (receiver), egress counts eth->h_source (sender)
- Ethernet bounds check before MAC dereference: if ((void *)(eth + 1) > data_end) return TC_ACT_OK;
- clang required for bpf2go compilation; generate.go detects clang absence gracefully
- Go-side types mirror C structs for layout verification
- SPDX license identifier required on all eBPF C source files
- bpf2go expects single translation unit; both programs in ingress.c
- cflags after -- must be separate exec.Command arguments, not one combined string
- ingress.c contains both SEC("tc/ingress") and SEC("tc/egress") with shared map

## 2026-04-13T20:00:00Z - Task 3 SQLite schema implementation (reimplemented)
- Implemented SQLite schema with three tables using WITHOUT ROWID for performance:
  - `mac_traffic_totals`: all-time totals unique on `(interface_name, ifindex, mac)` - no daily scan needed for all-time queries
  - `mac_traffic_daily`: UTC daily buckets unique on `(interface_name, ifindex, mac, date_utc)`
  - `collector_checkpoints`: last-seen raw counters for crash-safe recovery, unique on `(interface_name, ifindex, mac)`
- Used SQLite `ON CONFLICT DO UPDATE SET ... = ... + excluded.*` pattern for deterministic transactional upsert
- WAL mode via `PRAGMA journal_mode = WAL` with busy_timeout=5000
- Added indexes: `idx_daily_date_utc`, `idx_totals_interface`, `idx_daily_interface`, `idx_checkpoints_interface`
- Repository exposes: `UpsertDaily`, `UpsertTotal`, `UpsertCheckpoint`, `GetDaily`, `GetTotal`, `GetCheckpoint`, `ListDaily`, `ListTotals`, `ListCheckpoints`, `PruneDaily`
- UTC date functions: `CurrentDateUTC()`, `FormatDateUTC()`, `PruneCutoffDate()` (400 days ago)
- All upserts use explicit `tx.BeginTx()` / `tx.Commit()` / `tx.Rollback()` pattern
- Tests: 13 passing tests covering schema init, upsert accumulation, pruning, checkpoint recovery, UTC semantics
- All-time totals NOT affected by daily pruning - separate table ensures independent retention

## 2026-04-13T12:00:00Z - Task 4 configuration and startup contract (reimplemented)
- Implemented strict config validation with actionable error messages for all fields
- Config supports: interface allowlist, bind address (loopback-only), database path, flush interval (positive), log level (debug/info/warn/error), allow_partial_attach
- Implemented startup validation that checks capabilities (root), interface existence, and TC qdisc prerequisites
- Runtime contract implemented with three modes: healthy (all attached), degraded (partial with allow_partial=true), failed (zero attaches or partial with allow_partial=false)
- Main.go wired to load config, validate, bootstrap validate, and manage service lifecycle
- JSON config file parsing using encoding/json with proper error handling
- Default config uses UTC semantics and loopback-only bind address (127.0.0.1:8080)
- StartupResult tracks failed interfaces and error messages for explicit degraded state visibility
- Service status updated from bootstrap result at startup
- Dependency injection for environment checks enables deterministic unit testing without root privileges
- Runtime contract: degraded mode when partial attach + allow_partial=true; fail-fast when zero attach or partial attach + allow_partial=false

## 2026-04-13T21:00:00Z - Task 5 eBPF loader implementation (reimplemented)
- cilium/ebpf link package uses link.AttachTCX() not link.AttachTC() - TC attachment via TCX
- link.TCXOptions{Interface, Program, Attach} where Attach is ebpf.AttachTCXIngress or ebpf.AttachTCXEgress
- rlimit.RemoveMemlock() needed before loading eBPF objects (MEMLOCK rlimit)
- ebpf.Collection and ebpf.Map types are from github.com/cilium/ebpf
- TrafficMap wrapper created with m *ebpf.Map field (map is Go keyword)
- Mock mode when clang unavailable: loader.isMockMode() checks clang PATH and object file existence
- AttachmentManager tracks per-interface + per-direction state: IfaceState{Ingress, Egress} each with DirectionState{State, Link, ErrorMsg}
- Attach idempotency: attachDirection() checks existing StateAttached before re-attaching
- DetachAll() iterates all tracked interfaces and calls DetachIface() for each
- Stop() cleanup sets all interface states to StateDetached after close
- LoadAndAssign with inline struct (not named type) avoids LoadAndAssign type mismatch issues

(End of file - total 152 lines)

## 2026-04-13T19:45:00Z - Task 10 Window and Restart Verification

### Window semantics verified:
- 7days: start = today - 6, end = today (inclusive) - NOT today-7
- 30days: start = today - 29, end = today (inclusive) - NOT today-30
- month: start = first day of current calendar month, end = today
- today: start = today, end = today
- all: returns all-time totals directly

### Off-by-one errors in tests:
- My initial test data used "7 days ago" which is today-7, but 7days window starts at today-6
- Similarly "30 days ago" (today-30) is outside the 30days window (today-29 through today)
- Corrected test data to use today-6 and today-29 respectively

### Checkpoint-based delta computation:
- Flush loop stores last_seen raw counter values in checkpoint table
- On next flush: delta = current_raw - checkpoint_last_raw
- This ensures no double-counting after restart
- If day changed or no checkpoint: delta = full raw value (handles rollover)

### Key test functions added:
- TestAllWindowVerification: all-time totals aggregation
- TestTodayWindowVerification: single day query
- Test7DaysWindowVerification: sliding 7-day window
- Test30DaysWindowVerification: sliding 30-day window  
- TestMonthWindowVerification: calendar month window
- TestRestartCheckpointRecovery: restart without double-count
- TestFlushLoopDeltaComputation: delta calculation correctness
- TestMultiInterfaceSameMacAPILevel: same MAC on different ifaces

## 2026-04-13T20:00:00Z - Task 10 test file naming fix

### Root cause of test discoverability issue:
- File named `e2e_windows_test.go` is treated as OS-specific by Go build system
- Go interprets `_windows_test.go` suffix as `//go:build windows` constraint
- Tests were compiled only when GOOS=windows, not on Linux

### Fix applied:
- Renamed `integration/e2e_windows_test.go` to `integration/e2e_verification_test.go`
- Non-suffixed `_test.go` files are included on all platforms

### Verification:
- `go test -list . ./integration` now shows all 19 tests (14 new + 5 original)
- `go test -count=1 ./integration` → PASS
- `go test -count=1 ./...` → All packages pass

## 2026-04-13T22:00:00Z - Task 11 UTC rollover and retention pruning

### HousekeepingLoop design:
- Separate loop from FlushLoop for orthogonal concerns (persistence vs maintenance)
- Interval-based scheduling with 1-hour default (configurable via HousekeepingInterval)
- Runs once at Start() to catch any missed pruning on startup
- runOnce() calls storage.PruneDaily(cutoffDateUTC) where cutoffDateUTC = time.Now().UTC().AddDate(0,0,-400)
- DELETE WHERE date_utc < cutoff (strict less-than preserves cutoff day itself)

### UTC rollover in FlushLoop:
- currentDateUTC := storage.CurrentDateUTC() called once at flush start
- Checkpoint date comparison: cp.DateUTC == currentDateUTC
- If same UTC day: delta = current - checkpoint_last
- If new UTC day: delta = raw counters (wrapping handled)
- persistAll writes to daily bucket keyed by currentDateUTC

### Month window UTC boundary:
- queryMonth uses time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC) for start-of-month
- FormatDateUTC converts to YYYY-MM-DD string for SQL
- SQL: WHERE date_utc >= 'YYYY-MM-01' AND date_utc <= 'today'
- Lexicographic string comparison matches chronological order for YYYY-MM-DD format

### Pruning preserves all-time totals:
- PruneDaily only touches mac_traffic_daily table
- mac_traffic_totals is completely independent - never pruned
## 2026-04-13T23:00:00Z - Task 12 operational packaging and runbook

### README.md coverage:
- Complete operator documentation with build, run, configuration, API, and limitations
- API endpoints documented with exact paths and query parameters matching implementation
- Service modes table (healthy/degraded/failed) with exact conditions
- Makefile targets table with all 8 targets verified against actual Makefile
- Limitations section explicitly notes bridge/VLAN/bond unsupported (no dedup semantics)
- File layout section documenting all key directories

### API endpoint verification:
- GET /healthz: returns 200 only when IsHealthy() AND !IsStale(), 503 otherwise
- GET /api/v1/status: returns mode, configured_ifaces, attached_ifaces, failed_ifaces, last_flush_timestamp, flush_error, database_path
- GET /api/v1/traffic: window (all/7days/30days/today/month), interface filter, mac filter (XX:XX:XX:XX:XX:XX regex), limit (1-1000), offset

### Loopback config example:
- Created examples/config-loopback.json with "lo" interface and /tmp/traffic-count-test.db
- Suitable for testing without real network interfaces

### Makefile target verification:
- All targets functional: build, build-ebpf, build-binary, test, clean, run, verify-build-order, verify-layout
- Build passes: go build -o bin/traffic-count ./cmd/traffic-count
- Tests pass: go test ./...
