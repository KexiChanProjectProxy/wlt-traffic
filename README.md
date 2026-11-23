# traffic-count (wlt-traffic)

Per-device LAN traffic accounting with eBPF. It is the traffic backend of
[wlt](https://github.com/KexiChanProjectProxy/wlt): wlt shows each device its
own usage and gives the admin a ranking.

Chinese documentation: [docs/zh/README.md](docs/zh/README.md).

## How it counts

Two tc programs are attached to each configured LAN interface (normally the
LAN bridge, e.g. `br-lan`). Directions are from the **device's** point of view:

| hook on the LAN interface | frame | device identified by | counted as |
|---|---|---|---|
| ingress | device → router | Ethernet **source** MAC | upload |
| egress  | router → device | Ethernet **destination** MAC | download |

- Multicast/broadcast frames are not attributed to any device.
- Bytes are `skb->len` (L2 frame without FCS); packets use GSO segment counts.
- Counters are per-CPU (no lost updates) in an LRU hash keyed by
  `(ifindex, MAC)`; the key has explicit, zeroed padding.
- The programs return `TC_ACT_UNSPEC`/`TCX_NEXT`, so other TCX programs and
  legacy tc filters (SQM, ifb redirects) keep running.
- TCX is used on kernels ≥ 6.6; older kernels fall back to clsact + cls_bpf.
- When a configured interface is deleted and re-created (bridges often are),
  the programs are re-attached automatically.

Every `flush_interval` seconds the service reads the counters, subtracts the
previous reading (kept in memory, since the kernel map lives exactly as long as
the process) and adds the increase to SQLite under the current **local** day.
Writes that fail are retried; a final flush runs on shutdown.

Only list the bridge, not its member ports as well, or bridged frames are
counted twice. Non-Ethernet interfaces (WireGuard, tun) have no MAC addresses
and are rejected.

## Build

```sh
make build      # bin/traffic-count; the BPF object is embedded
make test
make e2e        # root: real-kernel test in network namespaces (TCX and tc)
make generate   # only after editing bpf/traffic.c; needs clang + libbpf-dev
make deb        # ../traffic-count_<version>_<arch>.deb for this machine
```

## Configuration

`/etc/traffic-count/config.json`:

```json
{
  "interfaces": ["br-lan"],
  "bind_address": "127.0.0.1:8081",
  "database_path": "/var/lib/traffic-count/traffic-count.db",
  "flush_interval": 10,
  "housekeeping_interval": 3600,
  "retention_days": 400,
  "timezone": "Local",
  "allow_partial": false
}
```

| key | default | meaning |
|---|---|---|
| `interfaces` | (required) | LAN-facing L2 interfaces to count on |
| `bind_address` | `127.0.0.1:8081` | API address; must be loopback |
| `database_path` | `/var/lib/traffic-count/traffic-count.db` | SQLite file |
| `flush_interval` | `10` | seconds between samples / writes |
| `housekeeping_interval` | `3600` | seconds between prunes of old daily rows |
| `retention_days` | `400` | daily rows kept; totals are kept forever |
| `timezone` | `Local` | IANA name (e.g. `Asia/Shanghai`); where a day starts |
| `allow_partial` | `false` | start even if some interfaces are missing (they attach when they appear) |

Unknown keys are rejected so a typo cannot silently fall back to a default.

## API

All responses are JSON. `upload` = sent by the device, `download` = received
by the device. Windows: `today`, `yesterday`, `7days`, `30days`, `month`,
`lastmonth`, `all`.

| endpoint | purpose |
|---|---|
| `GET /healthz` | 200 when all interfaces are attached and flushing works |
| `GET /api/v1/status` | interfaces (`tcx`/`tc`, errors), last flush, time zone |
| `GET /api/v1/traffic?window=&mac=&interface=&group=mac&sort=total&limit=&offset=` | ranked rows |
| `GET /api/v1/summary?mac=` | every window for one device (or all devices) in one call, plus live rate |
| `GET /api/v1/series?mac=&days=30` | daily points, oldest first, zero-filled |
| `GET /api/v1/live?mac=` | bytes/s per device over the last interval |

`/api/v1/traffic` record:

```json
{
  "interface": "br-lan", "mac": "aa:bb:cc:dd:ee:ff",
  "upload_bytes": 5024590, "download_bytes": 20025790, "total_bytes": 25050380,
  "upload_packets": 3736, "download_packets": 13892, "total_packets": 17628,
  "ingress_bytes": 5024590, "egress_bytes": 20025790,
  "ingress_packets": 3736, "egress_packets": 13892,
  "last_seen": 1795447871, "window": "today"
}
```

`ingress_*`/`egress_*` are the same numbers from the interface's point of view
(ingress = upload, egress = download) for older clients. With `group=mac` a
device's rows on several interfaces are summed and `interfaces` lists them.
Rows are sorted by `sort` (`total`, `upload`, `download`, `mac`) and the
response carries `total_count` for paging. `interface` may also be given as
`iface`.

## Upgrading from 1.x

- 1.x ignored every snake_case key in the config file (it parsed without JSON
  tags), so it listened on `127.0.0.1:8080` with `./traffic-count.db`. 2.x
  honours the file; check `bind_address` matches wlt's `traffic_api_url`.
- 1.x data is wrong (directions/devices mixed up, double counting) and is not
  migrated. New tables `device_traffic_daily` / `device_traffic_total` are
  created next to the old ones, which are left untouched.
- Clang, LLVM and libbpf are no longer needed on the target.
