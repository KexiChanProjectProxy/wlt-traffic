# Go eBPF MAC Traffic Accounting Service

## TL;DR
> **Summary**: Build a greenfield Go router/gateway daemon that attaches TC ingress and egress eBPF programs to a configurable interface allowlist, counts traffic by `(interface, MAC)`, persists authoritative rollups and collector checkpoint state to SQLite, and exposes REST endpoints for all-time and required calendar-based windows.
> **Deliverables**:
> - Go daemon with pure-Go `cilium/ebpf` loading
> - TC ingress + egress packet accounting by `(interface, MAC)`
> - SQLite persistence with all-time totals, UTC daily buckets, and collector checkpoints
> - REST API for all / 30 days / 7 days / today / this month
> - Config, health/status, retention pruning, and recovery behavior
> **Effort**: Large
> **Parallel**: YES - 3 waves
> **Critical Path**: 1 → 2 → 4 → 7 → 10

## Context
### Original Request
Design a golang based program,Using CGO or others way to load eBPF;the final target is count traffic on interface by mac,and Persistence to disk,and export a api to others program to query the traffic use,the api should able to show all,30days,7days,today,this month's traffic

### Interview Summary
- Repository is greenfield: `/home/kexi/traffic-count` is empty.
- Optimize for router/gateway deployment.
- Use REST over HTTP.
- Monitor a configurable allowlist of interfaces.
- Count both ingress and egress traffic; return combined total plus direction breakdown.
- Retain 400 UTC daily buckets and keep all-time totals indefinitely.
- Use tests-after, plus agent-executed QA scenarios in every task.

### Metis Review (gaps addressed)
- Locked v1 attachment strategy to TC ingress + TC egress only; no XDP or mixed-mode auto-selection.
- Locked accounting identity to `(interface, MAC)` to avoid cross-interface ambiguity.
- Locked time windows to UTC calendar semantics.
- Locked durability model to SQLite-backed all-time totals, daily rollups, and collector checkpoint state with explicit flush and recovery behavior.
- Added explicit degraded-mode, retention, and unsupported-topology guardrails.

## Work Objectives
### Core Objective
Produce a single-node Linux router/gateway daemon that passively accounts for per-MAC traffic on configured interfaces using eBPF, preserves results across restarts, and serves deterministic REST queries for required time windows.

### Deliverables
- Project skeleton for a Go service and eBPF object build flow
- eBPF C program(s) for TC ingress and TC egress accounting
- Userspace loader, attachment manager, and flush loop
- SQLite schema and repository layer
- REST HTTP API and health/status endpoints
- Configuration file/env model for interface allowlist, database path, bind address, and flush interval
- Automated tests and command-driven QA coverage

### Definition of Done (verifiable conditions with commands)
- `go test ./...` passes.
- `go build ./cmd/traffic-count` succeeds.
- `curl -sf http://127.0.0.1:8080/healthz` returns HTTP 200 when all configured interfaces are attached.
- `curl -sf http://127.0.0.1:8080/api/v1/traffic?window=today` returns JSON with totals and per-direction fields.
- Restarting the daemon preserves previously flushed all-time and daily data and restores the last persisted collector checkpoint.
- Retention pruning removes daily rows older than 400 UTC dates while preserving all-time totals.

### Must Have
- Pure-Go loader using `github.com/cilium/ebpf`
- TC ingress and egress programs attached only to configured allowlist interfaces
- Primary key semantics of `(interface, MAC)`
- UTC-based daily aggregation and calendar window queries
- SQLite as the only persistence backend for v1, including collector checkpoint state
- Explicit health/status reporting for attach failures and degraded mode

### Must NOT Have (guardrails, AI slop patterns, scope boundaries)
- No CGO/libbpf requirement in v1
- No XDP, netfilter, socket-filter, or mixed attach-mode auto-selection
- No packet modification, filtering, shaping, or enforcement logic
- No multi-node clustering, remote DB, auth, UI, websocket streaming, or Prometheus exporter in v1
- No promise of bridge/bond/VLAN deduplication beyond per-interface accounting
- No silent partial attach or silent counter loss; report degraded state explicitly

## Verification Strategy
> ZERO HUMAN INTERVENTION - all verification is agent-executed.
- Test decision: tests-after with Go `testing` package
- QA policy: Every task has agent-executed scenarios
- Evidence: `.sisyphus/evidence/task-{N}-{slug}.{ext}`

## Execution Strategy
### Parallel Execution Waves
> Target: 5-8 tasks per wave. <3 per wave (except final) = under-splitting.
> Extract shared dependencies as Wave-1 tasks for max parallelism.

Wave 1: 1 project skeleton/build tooling, 2 eBPF accounting design, 3 SQLite schema/repository, 4 configuration/boot contract
Wave 2: 5 loader/attach manager, 6 flush/aggregation loop, 7 REST API, 8 health/status and degraded-mode reporting
Wave 3: 9 test fixtures/traffic harness, 10 integration and persistence verification, 11 retention pruning and rollover, 12 packaging/docs/runbook

### Dependency Matrix (full, all tasks)
| Task | Depends On | Blocks |
|---|---|---|
| 1 | none | 5, 7, 9, 12 |
| 2 | 1 | 5, 6, 10 |
| 3 | 1 | 6, 7, 10, 11 |
| 4 | 1 | 5, 7, 8, 12 |
| 5 | 2, 4 | 6, 8, 10 |
| 6 | 2, 3, 5 | 7, 10, 11 |
| 7 | 3, 4, 6 | 10, 12 |
| 8 | 4, 5, 6, 7 | 10, 12 |
| 9 | 1, 2, 4 | 10 |
| 10 | 5, 6, 7, 8, 9 | 11, 12 |
| 11 | 3, 6, 10 | 12 |
| 12 | 4, 7, 8, 10, 11 | F1-F4 |

### Agent Dispatch Summary (wave → task count → categories)
- Wave 1 → 4 tasks → `quick`, `deep`, `unspecified-high`
- Wave 2 → 4 tasks → `deep`, `unspecified-high`
- Wave 3 → 4 tasks → `quick`, `deep`, `writing`, `unspecified-high`

## TODOs
> Implementation + Test = ONE task. Never separate.
> EVERY task MUST have: Agent Profile + Parallelization + QA Scenarios.

- [x] 1. Initialize the Go service skeleton and deterministic build pipeline

  **What to do**: Create the greenfield Go module, `cmd/traffic-count` entrypoint, internal package layout, Makefile targets, eBPF build integration, and configuration/bootstrap scaffolding. Standardize directories for `cmd/`, `internal/`, `bpf/`, `testdata/`, and runtime state. Ensure the build flow compiles eBPF objects before the Go binary and supports local development on Linux.
  **Must NOT do**: Do not implement business logic for counting or persistence beyond scaffolding. Do not add Docker, Kubernetes manifests, or CI beyond minimal local build/test targets unless strictly needed by the build chain.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: mostly deterministic project setup with clear outputs
  - Skills: `[]` - no special skill required
  - Omitted: `['playwright']` - no browser work involved

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: [2, 3, 4, 9, 12] | Blocked By: []

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/drafts/mac-traffic-ebpf-service.md:1-29` - captured technical decisions and guardrails
  - External: `https://ebpf-go.dev/` - project structure and build guidance for Go eBPF projects
  - External: `https://github.com/cilium/ebpf` - library conventions and examples

  **Acceptance Criteria** (agent-executable only):
  - [ ] `go mod init` has been run and `go test ./...` completes without package-resolution errors.
  - [ ] `make build` or equivalent deterministic command builds the Go binary and required eBPF artifact(s).
  - [ ] Repository layout includes dedicated paths for command entrypoint, internal packages, eBPF sources, and tests.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Build skeleton works
    Tool: Bash
    Steps: run `make build` in `/home/kexi/traffic-count`
    Expected: command exits 0 and produces the service binary plus compiled eBPF artifact(s)
    Evidence: .sisyphus/evidence/task-1-service-skeleton.txt

  Scenario: Tests discover packages cleanly
    Tool: Bash
    Steps: run `go test ./...` in `/home/kexi/traffic-count`
    Expected: command exits 0 with no missing module or package path errors
    Evidence: .sisyphus/evidence/task-1-service-skeleton-test.txt
  ```

  **Commit**: YES | Message: `feat(scaffold): initialize traffic counting service skeleton` | Files: [`go.mod`, `cmd/traffic-count/**`, `internal/**`, `bpf/**`, `Makefile`]

- [x] 2. Implement TC ingress and egress eBPF programs for `(interface, MAC)` byte accounting

  **What to do**: Create eBPF C sources and generated bindings that attach only at TC ingress and TC egress. Count bytes, packets, ingress bytes, ingress packets, egress bytes, and egress packets keyed by `(ifindex, mac[6])`. Define bounded maps with explicit max entries and a structure suitable for userspace batch reads. Count both source and destination MAC according to a fixed rule: ingress counts destination MAC as the local receiver on that interface; egress counts source MAC as the local sender on that interface.
  **Must NOT do**: Do not add XDP, socket filter, or flow-level accounting. Do not key by MAC alone. Do not try to deduplicate across bridges, VLANs, or bonds.

  **Recommended Agent Profile**:
  - Category: `deep` - Reason: kernel-facing logic with verifier constraints and low-level data structures
  - Skills: `[]` - no extra skill required
  - Omitted: `['frontend-ui-ux']` - irrelevant to kernel/user-space accounting

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: [5, 6, 9, 10] | Blocked By: [1]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/plans/mac-traffic-ebpf-service.md:1-92` - locked architecture, guardrails, and dependencies
  - External: `https://ebpf-go.dev/` - Go/eBPF build and generated object handling
  - External: `https://github.com/cilium/ebpf` - cilium/ebpf examples for TC attachment and map access
  - External: `https://docs.ebpf.io/linux/program-type/BPF_PROG_TYPE_SCHED_CLS/` - TC classifier program type semantics

  **Acceptance Criteria** (agent-executable only):
  - [ ] eBPF object compiles successfully through the project build command.
  - [ ] Map key includes both ifindex and MAC bytes.
  - [ ] Counter value includes per-direction and combined metrics required by the API layer.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: eBPF object compiles for tc hooks
    Tool: Bash
    Steps: run the project's eBPF build target and inspect generated object/bindings paths
    Expected: build exits 0 and emits object plus generated Go bindings for tc ingress/egress programs
    Evidence: .sisyphus/evidence/task-2-ebpf-build.txt

  Scenario: Key/value layout matches design
    Tool: Bash
    Steps: run generated-code or unit validation command that inspects key/value struct sizes and names
    Expected: validation confirms `(ifindex, mac)` key and direction-aware counter fields exist
    Evidence: .sisyphus/evidence/task-2-ebpf-layout.txt
  ```

  **Commit**: YES | Message: `feat(ebpf): add tc mac accounting programs` | Files: [`bpf/**`, `internal/ebpf/**`]

- [x] 3. Design the SQLite schema and repository layer for all-time totals and UTC daily buckets

  **What to do**: Implement SQLite migrations/schema and repository interfaces for two authoritative persisted stores: `mac_traffic_totals` for indefinite all-time totals and `mac_traffic_daily` for per-day UTC rollups. Use `(interface_name, ifindex, mac, date_utc)` for daily uniqueness and `(interface_name, ifindex, mac)` for totals. Add indexes needed for window queries and retention pruning. Store ingress, egress, combined bytes and packets. Define transactional upsert behavior.
  **Must NOT do**: Do not add alternate databases. Do not store raw packets or per-flow history. Do not use local time or rolling-hour storage.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: deterministic persistence model with standard repository patterns
  - Skills: `[]` - no special skill required
  - Omitted: `['playwright']` - no UI testing needed

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: [6, 7, 10, 11] | Blocked By: [1]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/drafts/mac-traffic-ebpf-service.md:11-28` - storage and time-window decisions
  - External: `https://www.sqlite.org/lang_upsert.html` - upsert semantics for durable rollups
  - External: `https://www.sqlite.org/pragma.html#pragma_journal_mode` - WAL mode and durability considerations

  **Acceptance Criteria** (agent-executable only):
  - [ ] Schema migration command creates required tables and indexes.
  - [ ] Repository tests verify upsert, query-by-window support primitives, and retention-delete behavior.
  - [ ] All-time totals remain queryable without scanning all daily rows.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: SQLite schema initializes correctly
    Tool: Bash
    Steps: run repository or migration tests against a temporary SQLite database file
    Expected: tests exit 0 and confirm tables/indexes exist with expected uniqueness constraints
    Evidence: .sisyphus/evidence/task-3-sqlite-schema.txt

  Scenario: Daily and total upserts are durable
    Tool: Bash
    Steps: run targeted repository tests that insert and update the same `(interface, mac)` and day repeatedly
    Expected: resulting rows show accumulated counters without duplicate-key failures
    Evidence: .sisyphus/evidence/task-3-sqlite-upsert.txt
  ```

  **Commit**: YES | Message: `feat(storage): add sqlite rollup schema and repository` | Files: [`internal/storage/**`, `migrations/**`]

- [x] 4. Define configuration, startup validation, and runtime contract

  **What to do**: Implement configuration loading for interface allowlist, HTTP bind address, database path, flush interval, log level, and degraded-mode policy. Define startup validation that checks required Linux capabilities, validates configured interfaces exist, verifies TC attach prerequisites, and refuses to start when zero interfaces attach successfully. Specify the explicit degraded-mode rule: if `allow_partial_attach=false`, exit non-zero on any configured interface attach failure; if `allow_partial_attach=true`, start and expose unhealthy/degraded status listing failed interfaces.
  **Must NOT do**: Do not auto-discover all interfaces. Do not silently downgrade features. Do not permit ambiguous defaults for time zone or persistence location.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: configuration and validation are straightforward but must be precise
  - Skills: `[]` - no special skill required
  - Omitted: `['git-master']` - no git operation involved

  **Parallelization**: Can Parallel: YES | Wave 1 | Blocks: [5, 7, 8, 9, 12] | Blocked By: [1]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/plans/mac-traffic-ebpf-service.md:33-66` - startup and degraded-mode guardrails
  - External: `https://github.com/cilium/ebpf/tree/main/rlimit` - memory lock handling for eBPF programs

  **Acceptance Criteria** (agent-executable only):
  - [ ] Invalid configuration fails fast with actionable errors.
  - [ ] Startup validation distinguishes zero-attach failure from partial-attach degraded mode.
  - [ ] Default configuration uses UTC semantics and a loopback-only bind address.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Invalid interface config fails startup
    Tool: Bash
    Steps: run the binary with a config referencing a nonexistent interface and `allow_partial_attach=false`
    Expected: process exits non-zero with an error naming the invalid interface
    Evidence: .sisyphus/evidence/task-4-config-invalid.txt

  Scenario: Partial attach degraded mode is explicit
    Tool: Bash
    Steps: run the binary with one valid and one invalid interface and `allow_partial_attach=true`, then query the status endpoint
    Expected: service starts, status reports degraded state, and failed interface is listed explicitly
    Evidence: .sisyphus/evidence/task-4-config-degraded.txt
  ```

  **Commit**: YES | Message: `feat(config): define startup contract and validation` | Files: [`internal/config/**`, `internal/bootstrap/**`, `cmd/traffic-count/**`]

- [x] 5. Implement the eBPF loader and TC attachment manager

  **What to do**: Build the userspace loader with `cilium/ebpf`, generated object loading, memlock handling, and TC attach/detach logic for ingress and egress on each configured interface. Ensure attach operations are idempotent, record per-interface attach state, and clean up all hooks on shutdown. Use ifindex from the live interface at startup but persist both interface name and ifindex in userspace records.
  **Must NOT do**: Do not auto-attach to non-allowlisted interfaces. Do not continue after total attach failure. Do not hide per-interface attach errors.

  **Recommended Agent Profile**:
  - Category: `deep` - Reason: runtime interaction with kernel hooks and lifecycle cleanup
  - Skills: `[]` - no special skill required
  - Omitted: `['frontend-ui-ux']` - no UI relevance

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: [6, 8, 10] | Blocked By: [2, 4]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/plans/mac-traffic-ebpf-service.md:36-66` - loader/attach constraints and degraded-mode rules
  - External: `https://github.com/cilium/ebpf` - object loading and map/program management
  - External: `https://docs.ebpf.io/linux/program-type/BPF_PROG_TYPE_SCHED_CLS/` - TC attach semantics

  **Acceptance Criteria** (agent-executable only):
  - [ ] Loader can attach both ingress and egress programs to each configured eligible interface.
  - [ ] Shutdown detaches all installed TC hooks cleanly.
  - [ ] Status state captures attached, failed, and detached interfaces deterministically.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Attach to configured interface succeeds
    Tool: Bash
    Steps: run the daemon against a known test interface in a Linux test environment, then query status
    Expected: status shows ingress and egress attached for the configured interface
    Evidence: .sisyphus/evidence/task-5-loader-attach.txt

  Scenario: Shutdown cleans up tc hooks
    Tool: Bash
    Steps: start the daemon, stop it gracefully, then inspect tc state on the configured interface
    Expected: no service-owned ingress or egress hooks remain after shutdown
    Evidence: .sisyphus/evidence/task-5-loader-cleanup.txt
  ```

  **Commit**: YES | Message: `feat(loader): add tc attachment lifecycle manager` | Files: [`internal/ebpf/**`, `internal/runtime/**`]

- [x] 6. Implement the flush loop, file-backed collector state, and crash-safe persistence flow

  **What to do**: Add a userspace flush loop that periodically reads counters from eBPF maps, computes deltas from the last persisted collector checkpoint, updates persisted current-day state, and writes three things in a single SQLite transaction: collector checkpoints, all-time totals, and current UTC daily bucket rows. Use a fixed flush interval default of 10 seconds. Store the last-seen raw counter values per `(interface, MAC)` in a dedicated checkpoint table so restart recovery resumes from disk-backed state rather than RAM. On startup, restore the last persisted checkpoints and current-day rows from SQLite, then continue delta computation from those file-backed values. On flush failure, keep the previous persisted checkpoint unchanged and retry on the next interval without advancing checkpoint state.
  **Must NOT do**: Do not write every packet individually to SQLite. Do not rely on RAM-only accumulators for correctness across restart. Do not discard unflushed deltas on transient DB failure. Do not use local time for rollover.

  **Recommended Agent Profile**:
  - Category: `deep` - Reason: correctness-sensitive delta accounting and durability behavior
  - Skills: `[]` - no special skill required
  - Omitted: `['playwright']` - runtime/data-path work only

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: [7, 8, 10, 11] | Blocked By: [2, 3, 5]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/drafts/mac-traffic-ebpf-service.md:11-27` - UTC, persistence, and totals decisions
  - External: `https://www.sqlite.org/lang_transaction.html` - transactional persistence semantics
  - External: `https://www.sqlite.org/wal.html` - WAL mode durability and concurrency

  **Acceptance Criteria** (agent-executable only):
  - [ ] Flush loop persists checkpoints, all-time totals, and the current UTC day bucket together.
  - [ ] Repeated flushes do not double-count unchanged map values.
  - [ ] Restarting after a successful flush restores file-backed checkpoint state and resumes accumulation correctly.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Periodic flush updates sqlite totals and checkpoints
    Tool: Bash
    Steps: generate controlled traffic for a test MAC, wait past one flush interval, then query the SQLite-backed API endpoint
    Expected: returned totals increase and checkpoint rows for that `(interface, mac)` are present in SQLite with advanced raw counters
    Evidence: .sisyphus/evidence/task-6-flush-loop.txt

  Scenario: Failed flush retries without advancing checkpoint state incorrectly
    Tool: Bash
    Steps: temporarily make the database unwritable during a flush, restore writability, wait for retry, then query totals
    Expected: totals reflect each traffic delta once, checkpoint state advances only on the successful retry, and status exposes a transient persistence error then recovery
    Evidence: .sisyphus/evidence/task-6-flush-retry.txt
  ```

  **Commit**: YES | Message: `feat(runtime): persist ebpf deltas into sqlite` | Files: [`internal/runtime/**`, `internal/storage/**`]

- [x] 7. Implement the REST API contract for traffic queries

  **What to do**: Expose HTTP endpoints on loopback by default with JSON responses. Implement `GET /api/v1/traffic` supporting filters `window`, optional `interface`, optional `mac`, and pagination for unfiltered listings. Implement allowed `window` values exactly: `all`, `30days`, `7days`, `today`, `month`. Return per-record fields: `interface`, `ifindex`, `mac`, `ingress_bytes`, `egress_bytes`, `total_bytes`, `ingress_packets`, `egress_packets`, `total_packets`, and `window`. For listing endpoints, aggregate from SQLite daily buckets plus the persisted current UTC day state/checkpointed rollup when needed. Return HTTP 400 for invalid `window` or malformed MAC.
  **Must NOT do**: Do not expose gRPC. Do not bind publicly by default. Do not invent alternate window names or local-time behavior.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: API contract, query semantics, and pagination need careful coordination
  - Skills: `[]` - no special skill required
  - Omitted: `['dev-browser']` - HTTP API validation does not require a browser

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: [8, 10, 12] | Blocked By: [3, 4, 6]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/plans/mac-traffic-ebpf-service.md:18-33` - required windows and query surface
  - Pattern: `.sisyphus/drafts/mac-traffic-ebpf-service.md:11-25` - exact UTC window definitions

  **Acceptance Criteria** (agent-executable only):
  - [ ] `GET /api/v1/traffic?window=all` returns valid JSON with required counter fields.
  - [ ] `GET /api/v1/traffic?window=today&mac=<mac>&interface=<name>` filters to the requested key.
  - [ ] Invalid MAC strings and invalid windows return HTTP 400 with deterministic error bodies.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Query today window for one mac/interface
    Tool: Bash
    Steps: send controlled traffic, then run `curl -sf "http://127.0.0.1:8080/api/v1/traffic?window=today&interface=<iface>&mac=<mac>"`
    Expected: JSON contains exactly one matching record with non-zero totals and correct direction fields
    Evidence: .sisyphus/evidence/task-7-api-today.json

  Scenario: Invalid mac is rejected
    Tool: Bash
    Steps: run `curl -s -o /tmp/task7.err -w "%{http_code}" "http://127.0.0.1:8080/api/v1/traffic?window=today&mac=not-a-mac"`
    Expected: HTTP status is 400 and body states MAC format is invalid
    Evidence: .sisyphus/evidence/task-7-api-invalid-mac.txt
  ```

  **Commit**: YES | Message: `feat(api): add persistence-backed traffic query endpoints` | Files: [`internal/http/**`, `internal/service/**`]

- [x] 8. Add health, status, and degraded-mode reporting

  **What to do**: Implement `GET /healthz` and `GET /api/v1/status`. `healthz` returns 200 only when all configured interfaces are attached and the most recent persistence flush is within 2x the configured flush interval; otherwise return 503. `status` returns JSON including configured interfaces, per-interface attach state, last successful flush timestamp, database path, current mode (`healthy`, `degraded`, `failed`), and last error summaries for attach/persistence failures.
  **Must NOT do**: Do not hide attach failures behind a generic healthy response. Do not couple health semantics to total traffic volume.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: status surface is deterministic once runtime state exists
  - Skills: `[]` - no special skill required
  - Omitted: `['frontend-ui-ux']` - API-only work

  **Parallelization**: Can Parallel: YES | Wave 2 | Blocks: [10, 12] | Blocked By: [4, 5, 6, 7]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/plans/mac-traffic-ebpf-service.md:44-66` - explicit degraded-mode and health guardrails

  **Acceptance Criteria** (agent-executable only):
  - [ ] `/healthz` returns 200 in healthy state and 503 in degraded or stale-flush state.
  - [ ] `/api/v1/status` includes per-interface attach results and last flush metadata.
  - [ ] Partial-attach startup with `allow_partial_attach=true` is visible through status output.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Healthy service reports 200
    Tool: Bash
    Steps: start the daemon with valid config and query `curl -sf -o /tmp/health.out -w "%{http_code}" http://127.0.0.1:8080/healthz`
    Expected: HTTP status is 200 and status endpoint reports `healthy`
    Evidence: .sisyphus/evidence/task-8-health-healthy.txt

  Scenario: Stale flush triggers unhealthy state
    Tool: Bash
    Steps: simulate a blocked persistence loop past 2x flush interval, then query `/healthz` and `/api/v1/status`
    Expected: health returns 503 and status reports degraded mode with persistence failure details
    Evidence: .sisyphus/evidence/task-8-health-stale.txt
  ```

  **Commit**: YES | Message: `feat(status): expose health and degraded state` | Files: [`internal/http/**`, `internal/runtime/**`]

- [x] 9. Build deterministic test fixtures and traffic-generation harness

  **What to do**: Create unit-test fixtures and an integration harness that can generate controlled L2/L3 traffic across Linux test interfaces or namespaces. Include helpers to create temporary veth pairs or equivalent isolated interfaces for automated tests, seed known MAC addresses, and generate both ingress and egress traffic. Provide reusable helpers for starting the daemon with a temporary SQLite DB and querying the REST API.
  **Must NOT do**: Do not require manual packet crafting. Do not depend on external network access or a browser. Do not make tests rely on host-specific interfaces by default.

  **Recommended Agent Profile**:
  - Category: `unspecified-high` - Reason: integration harness must be robust and reproducible across Linux environments
  - Skills: `[]` - no special skill required
  - Omitted: `['playwright']` - no browser interaction needed

  **Parallelization**: Can Parallel: YES | Wave 3 | Blocks: [10] | Blocked By: [1, 2, 4]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/plans/mac-traffic-ebpf-service.md:68-92` - verification strategy and wave dependencies
  - External: `https://pkg.go.dev/testing` - Go testing harness conventions

  **Acceptance Criteria** (agent-executable only):
  - [ ] Test helpers can create isolated interfaces/namespaces and produce deterministic traffic for a known MAC.
  - [ ] Integration tests can start the daemon against a temporary SQLite file and temporary config.
  - [ ] Harness cleanup removes created interfaces/namespaces after test completion.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Traffic harness generates reproducible bytes
    Tool: Bash
    Steps: run the dedicated integration-harness test suite that creates test interfaces and emits fixed-size traffic
    Expected: tests exit 0 and report deterministic sent-byte counts for the known MAC and interface
    Evidence: .sisyphus/evidence/task-9-harness.txt

  Scenario: Harness cleanup is reliable
    Tool: Bash
    Steps: run the harness test suite twice back-to-back
    Expected: second run succeeds without leftover interface/namespace conflicts
    Evidence: .sisyphus/evidence/task-9-harness-cleanup.txt
  ```

  **Commit**: YES | Message: `test(harness): add reproducible traffic fixtures` | Files: [`internal/testutil/**`, `testdata/**`, `integration/**`]

- [x] 10. Verify end-to-end counting, persistence recovery, and window correctness

  **What to do**: Add integration tests that run the full daemon against the harness, generate traffic for one or more MACs, and verify API results for `all`, `30days`, `7days`, `today`, and `month`. Include restart tests proving that persisted data and collector checkpoints survive process restarts, and multi-interface tests proving the same MAC on different interfaces remains distinct. Seed historical SQLite rows directly for 7-day/30-day/month verification where needed instead of waiting on real-time passage.
  **Must NOT do**: Do not rely on wall-clock waiting for multi-day tests. Do not assume MAC-only uniqueness. Do not require human inspection to validate outputs.

  **Recommended Agent Profile**:
  - Category: `deep` - Reason: this is the core correctness gate for the entire system
  - Skills: `[]` - no special skill required
  - Omitted: `['dev-browser']` - HTTP queries can be validated with Bash and Go tests

  **Parallelization**: Can Parallel: YES | Wave 3 | Blocks: [11, 12] | Blocked By: [5, 6, 7, 8, 9]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/drafts/mac-traffic-ebpf-service.md:11-27` - precise window and persistence semantics
  - Pattern: `.sisyphus/plans/mac-traffic-ebpf-service.md:24-32` - definition of done commands

  **Acceptance Criteria** (agent-executable only):
  - [ ] End-to-end test proves non-zero ingress and egress accounting for a known `(interface, MAC)`.
  - [ ] Restart test proves persisted all-time and daily values survive daemon restart.
  - [ ] Window tests prove `today`, `7days`, `30days`, `month`, and `all` return the expected sums under UTC semantics.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: End-to-end query windows are correct
    Tool: Bash
    Steps: run the full integration suite covering seeded historical rows plus live traffic, then capture the test report
    Expected: suite exits 0 and includes passing assertions for `all`, `30days`, `7days`, `today`, and `month`
    Evidence: .sisyphus/evidence/task-10-e2e-windows.txt

  Scenario: Restart preserves flushed counters and checkpoint state
    Tool: Bash
    Steps: start daemon, generate traffic, wait for flush, stop daemon, restart daemon, query `window=all` for the same `(interface, mac)`
    Expected: post-restart totals are greater than or equal to pre-restart flushed totals, checkpoint rows are restored from SQLite, and no reset to zero occurs
    Evidence: .sisyphus/evidence/task-10-e2e-restart.txt
  ```

  **Commit**: YES | Message: `test(integration): cover persistence and query windows` | Files: [`integration/**`, `internal/http/**`, `internal/storage/**`]

- [x] 11. Implement UTC rollover and retention pruning behavior

  **What to do**: Add a scheduled housekeeping path that detects UTC date change, ensures new daily buckets are used for the new date, and prunes daily rows older than 400 UTC dates. Keep all-time totals untouched. Add tests for month boundary behavior and pruning cutoffs. Use SQL queries that compute date cutoffs deterministically from UTC.
  **Must NOT do**: Do not prune all-time totals. Do not use local timezone or approximate 30-day arithmetic for calendar-month logic.

  **Recommended Agent Profile**:
  - Category: `quick` - Reason: bounded housekeeping logic with deterministic test cases
  - Skills: `[]` - no special skill required
  - Omitted: `['playwright']` - no browser involvement

  **Parallelization**: Can Parallel: YES | Wave 3 | Blocks: [12] | Blocked By: [3, 6, 10]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/drafts/mac-traffic-ebpf-service.md:17-24` - retention and UTC definitions
  - External: `https://www.sqlite.org/lang_datefunc.html` - date handling guidance for SQLite

  **Acceptance Criteria** (agent-executable only):
  - [ ] Rows older than 400 UTC dates are pruned automatically.
  - [ ] Current month and today queries use UTC boundaries exactly.
  - [ ] Pruning does not alter all-time totals.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Retention pruning removes only old daily rows
    Tool: Bash
    Steps: seed a SQLite test database with more than 400 UTC daily rows, run housekeeping, then inspect row counts and totals
    Expected: rows older than cutoff are removed, rows at/after cutoff remain, all-time totals are unchanged
    Evidence: .sisyphus/evidence/task-11-pruning.txt

  Scenario: Month boundary uses UTC correctly
    Tool: Bash
    Steps: run targeted tests with seeded rows spanning two months and query `window=month`
    Expected: response includes only rows from the current UTC calendar month plus persisted current-day state
    Evidence: .sisyphus/evidence/task-11-month-boundary.txt
  ```

  **Commit**: YES | Message: `feat(retention): add utc rollover and pruning` | Files: [`internal/runtime/**`, `internal/storage/**`]

- [x] 12. Finalize operational packaging, configuration examples, and executor runbook

  **What to do**: Add sample config files, README/runbook content, Makefile targets, and service execution guidance covering required Linux capabilities, expected bind address defaults, database path behavior, and supported/unsupported topologies. Document exact API endpoints, query parameters, degraded-mode semantics, and restart/recovery behavior. Keep docs limited to this service’s operation.
  **Must NOT do**: Do not add deployment artifacts for Docker/Kubernetes/systemd unless required separately. Do not promise unsupported topology semantics.

  **Recommended Agent Profile**:
  - Category: `writing` - Reason: documentation and operator guidance require precision and consistency
  - Skills: `[]` - no special skill required
  - Omitted: `['frontend-ui-ux']` - documentation only

  **Parallelization**: Can Parallel: YES | Wave 3 | Blocks: [F1, F2, F3, F4] | Blocked By: [1, 4, 7, 8, 10, 11]

  **References** (executor has NO interview context - be exhaustive):
  - Pattern: `.sisyphus/plans/mac-traffic-ebpf-service.md:1-221` - full execution contract and guardrails
  - External: `https://github.com/cilium/ebpf` - operator-facing dependency context for capabilities/build expectations

  **Acceptance Criteria** (agent-executable only):
  - [ ] Sample config can start the service on loopback with a temporary SQLite DB and explicit interface allowlist.
  - [ ] Documentation lists required capabilities, supported interface scope, API endpoints, and degraded-mode semantics.
  - [ ] Build/test/run commands in docs are validated against the repository state.

  **QA Scenarios** (MANDATORY - task incomplete without these):
  ```
  Scenario: Documented run path is accurate
    Tool: Bash
    Steps: follow the documented build and run commands using the sample config in a Linux test environment
    Expected: commands work as written and the service reaches either healthy or explicitly degraded status according to config
    Evidence: .sisyphus/evidence/task-12-runbook.txt

  Scenario: API documentation matches implementation
    Tool: Bash
    Steps: compare documented endpoints and query parameters with live responses from the running service
    Expected: every documented endpoint exists and undocumented endpoints are not required for core workflow
    Evidence: .sisyphus/evidence/task-12-api-docs.txt
  ```

  **Commit**: YES | Message: `docs(runbook): document traffic accounting service operation` | Files: [`README.md`, `configs/**`, `Makefile`]

## Final Verification Wave (MANDATORY — after ALL implementation tasks)
> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.
> **Do NOT auto-proceed after verification. Wait for user's explicit approval before marking work complete.**
> **Never mark F1-F4 as checked before getting user's okay.** Rejection or user feedback -> fix -> re-run -> present again -> wait for okay.
- [x] F1. Plan Compliance Audit — oracle
- [x] F2. Code Quality Review — unspecified-high
- [x] F3. Real Manual QA — unspecified-high (+ playwright if UI)
- [x] F4. Scope Fidelity Check — deep

## Commit Strategy
- Commit after each completed wave.
- Preferred commit sequence:
  - `feat(scaffold): initialize traffic counting service skeleton`
  - `feat(ebpf): add tc mac accounting pipeline`
  - `feat(api): add persistence-backed traffic query endpoints`
  - `test(integration): cover persistence, rollover, and degraded mode`

## Success Criteria
- The service can be started on a Linux router/gateway with required capabilities and a configured interface allowlist.
- Traffic for each `(interface, MAC)` is counted for ingress and egress and persisted to disk.
- Query API returns deterministic totals for all required windows using UTC semantics.
- Restart, partial attach failure, and pruning behaviors are visible and correct.
