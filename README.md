# traffic-count

MAC-level traffic accounting service using eBPF TC (traffic control) classifiers.

## What it does

The service attaches eBPF programs to network interfaces to capture per-MAC ingress/egress byte and packet counters. Counters are periodically flushed to SQLite for persistence.

## Build

```
make build
```

This produces `bin/traffic-count`.

Requirements:
- Go 1.21+
- clang (for eBPF object generation; without it the service runs in mock mode)

## Run

```
./bin/traffic-count --config examples/config.json
```

The service requires root privileges to attach eBPF programs and interrogate network interfaces.

## Configuration

```json
{
  "interfaces": ["eth0", "wg0"],
  "bind_address": "127.0.0.1:8080",
  "database_path": "/var/lib/traffic-count/traffic-count.db",
  "flush_interval": 10,
  "housekeeping_interval": 3600,
  "log_level": "info",
  "allow_partial": false
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `interfaces` | yes | Interface allowlist. Service attaches to these interfaces only. |
| `bind_address` | yes | HTTP server bind address. Must be loopback (127.0.0.1, localhost, or ::1). |
| `database_path` | yes | SQLite database file path. |
| `flush_interval` | yes | Seconds between counter flushes to SQLite. Must be positive. |
| `housekeeping_interval` | no | Seconds between housekeeping runs (default 3600). Prunes daily records older than 400 days. |
| `log_level` | no | One of: debug, info, warn, error (default info). |
| `allow_partial` | no | Allow degraded mode when some interfaces fail to attach (default false). |

## API

### GET /healthz

Liveness probe. Returns 200 if service is healthy and flush loop is not stale. Returns 503 otherwise.

### GET /api/v1/status

Returns service status as JSON.

```json
{
  "mode": "healthy",
  "configured_ifaces": ["eth0", "wg0"],
  "attached_ifaces": ["eth0", "wg0"],
  "failed_ifaces": [],
  "last_flush_timestamp": 1744567800,
  "flush_error": "",
  "database_path": "/var/lib/traffic-count/traffic-count.db"
}
```

### GET /api/v1/traffic

Query traffic records.

Query parameters:
- `window` - Time window: `all`, `today`, `7days`, `30days`, `month` (default: today)
- `interface` - Filter by interface name (optional)
- `mac` - Filter by MAC address XX:XX:XX:XX:XX:XX (optional)
- `limit` - Max records to return, 1-1000 (default 100)
- `offset` - Pagination offset (default 0)

```json
{
  "window": "today",
  "limit": 100,
  "offset": 0,
  "records": [
    {
      "interface": "eth0",
      "ifindex": 2,
      "mac": "aa:bb:cc:dd:ee:ff",
      "ingress_bytes": 1234,
      "egress_bytes": 5678,
      "total_bytes": 6912,
      "ingress_packets": 10,
      "egress_packets": 20,
      "total_packets": 30,
      "window": "today"
    }
  ]
}
```

## Service modes

| Mode | Condition |
|------|-----------|
| healthy | All configured interfaces attached successfully. |
| degraded | Some interfaces attached, some failed, and `allow_partial=true`. |
| failed | Zero interfaces attached, OR some failed with `allow_partial=false`. |

A service in degraded mode continues running. A service in failed mode exits immediately at startup.

## Operational notes

**Flush loop** persists counter deltas to SQLite at the configured interval. On restart, checkpoints are restored to avoid double-counting.

**Housekeeping loop** runs at the configured interval and prunes `mac_traffic_daily` rows older than 400 days (UTC). `mac_traffic_totals` is never pruned.

**Stale flush detection**: If the flush loop has not run successfully within 2x the flush interval, the `/healthz` endpoint returns 503 even if interfaces are healthy.

**Daily UTC windows**: The `today` window uses the current UTC date. `7days` and `30days` are inclusive sliding windows. `month` runs from the first day of the current calendar month to today.

## Limitations

The service is designed for flat L2 interface allowlists. Bridge, VLAN, and bonding topologies are not supported. Dedup semantics across those topologies are undefined.

Mock mode (without clang/eBPF headers) allows full service lifecycle testing but produces no real traffic counters.

## Makefile targets

| Target | Description |
|--------|-------------|
| `make build` | Build eBPF objects and Go binary |
| `make build-ebpf` | Generate eBPF bindings only |
| `make build-binary` | Build Go binary only |
| `make test` | Run all tests |
| `make clean` | Remove build artifacts |
| `make run` | Build and run the service |
| `make verify-build-order` | Print build order info |
| `make verify-layout` | Print key/value layout info |

## File layout

```
cmd/traffic-count/main.go      # Entry point
internal/
  bootstrap/                   # Startup validation
  config/                      # Configuration loading
  ebpf/                        # eBPF loader and attachment
  http/                        # HTTP API server
  runtime/                     # Service, flush loop, housekeeping
  storage/                     # SQLite repository
examples/
  config.json                  # Example configuration
  config-loopback.json         # Loopback-only example for testing
  traffic-count.service        # systemd unit example
bpf/
  ingress.c                    # eBPF TC ingress program
  egress.c                     # eBPF TC egress program
  shared.h                     # Shared map definitions
```
