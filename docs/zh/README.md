# traffic-count 服务文档

## 目录

1. [项目概述](#1-项目概述)
2. [系统架构](#2-系统架构)
3. [核心概念](#3-核心概念)
4. [eBPF 技术详解](#4-ebpf-技术详解)
5. [数据模型与存储](#5-数据模型与存储)
6. [HTTP API 参考](#6-http-api-参考)
7. [配置指南](#7-配置指南)
8. [部署指南](#8-部署指南)
9. [运行与维护](#9-运行与维护)
10. [故障排查](#10-故障排查)

---

## 1. 项目概述

### 1.1 是什么

`traffic-count` 是一个基于 eBPF TC (Traffic Control) 分类器的 MAC 层流量记账服务。它通过在内核网络接口上附加 eBPF 程序，实时捕获每个 MAC 地址的入站/出站字节数和报文数，并将计数器周期性地刷新到 SQLite 数据库进行持久化存储。

### 1.2 核心能力

| 能力 | 说明 |
|------|------|
| **MAC 级计数** | 以 MAC 地址为维度，分别统计 ingress/egress 的 bytes 和 packets |
| **多接口支持** | 可同时监控多个网络接口（网卡、WireGuard 等） |
| **非侵入式** | 使用 eBPF TC 附加点，无需修改内核或重新编译 |
| **增量持久化** | 通过 checkpoint 机制实现断点恢复，避免重复计数 |
| **每日桶聚合** | 数据按 UTC 日期分桶存储，支持按时间窗口查询 |
| **自动清理** | Housekeeping 循环自动删除 400 天前的历史数据 |

### 1.3 技术栈

| 层级 | 技术 |
|------|------|
| 数据平面 | eBPF (TCX ingress/egress) |
| 控制平面 | Go 1.21+ / cilium/ebpf |
| 数据持久化 | SQLite (WAL 模式, WITHOUT ROWID) |
| HTTP API | Go 标准库 net/http |

### 1.4 适用场景

- 网络监控与计量
- 按 MAC 地址统计流量使用量
- WireGuard VPN 流量统计
- 透明防火墙/网关的流量日志

### 1.5 限制与不适用场景

- **不支持**网桥（Bridge）、VLAN、Bonding 等复杂拓扑的重复数据消除
- 设计为**平面 L2 接口白名单**模式
- 需要 root 权限运行（CAPE_SYS_ADMIN、CAPE_NET_ADMIN）
- 无 clang/eBPF 头文件时服务以 **Mock 模式**运行（仅用于测试，不产生真实计数）

---

## 2. 系统架构

### 2.1 整体架构图

```
┌─────────────────────────────────────────────────────────────────┐
│                      traffic-count 服务进程                       │
│                                                                 │
│  ┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐   │
│  │ Bootstrap │   │  Config  │   │ HTTP API │   │ Storage  │   │
│  │ Validator │   │  Loader  │   │ Server   │   │(SQLite)  │   │
│  └────┬─────┘   └────┬─────┘   └────┬─────┘   └────┬─────┘   │
│       │              │              │              │           │
│       └──────────────┴──────────────┴──────────────┘           │
│                            │                                    │
│  ┌─────────────────────────┴──────────────────────────────┐    │
│  │                      Runtime Service                      │    │
│  │  ┌────────────────┐          ┌────────────────────────┐  │    │
│  │  │  Flush Loop   │          │   Housekeeping Loop    │  │    │
│  │  │ (定期刷新到DB)  │          │   (每日清理/UTC换桶)    │  │    │
│  │  └───────┬────────┘          └───────────┬───────────┘  │    │
│  └──────────┼────────────────────────────────┼──────────────┘    │
│             │           ┌──────────┐         │                   │
│             └───────────┤  Loader  ├─────────┘                   │
│                         └────┬─────┘                              │
│  ┌───────────────────────────┴────────────────────────────────┐  │
│  │              AttachmentManager (ifaceStates map)            │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                    TrafficMap (eBPF HASH)                   │  │
│  │              key: {ifindex(4) + mac(6)} = 10B              │  │
│  │           value: {6 x uint64 counters} = 48B              │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
                              │
         ┌────────────────────┼────────────────────┐
         ▼                    ▼                    ▼
   ┌──────────┐         ┌──────────┐         ┌──────────┐
   │   eth0   │         │   wg0    │         │  other   │
   │  TCX ing │         │  TCX ing │         │   ...    │
   │  TCX eg  │         │  TCX eg  │         │
   └──────────┘         └──────────┘
         │                    │                    │
         ▼                    ▼                    ▼
   ┌─────────────────────────────────────────────────────────┐
   │                     Linux Kernel                        │
   │              net/core/flow_dissector.c                   │
   │                  (eBPF Subsystem)                        │
   └─────────────────────────────────────────────────────────┘
```

### 2.2 数据流

```
数据包进入接口
       │
       ▼
┌──────────────────┐
│  eBPF TC Classifier │
│  SEC("tc/ingress") │
│  SEC("tc/egress")  │
└────────┬──────────┘
         │ 更新 traffic_map[ {ifindex, mac} ]
         ▼
┌──────────────────┐
│   eBPF Hash Map  │◄──── 内存中计数
│ traffic_map      │
│ (262144 slots)   │
└────────┬──────────┘
         │ Flush Loop (每 flush_interval 秒)
         ▼
┌──────────────────┐     ┌────────────────────┐
│  SQLite Database │────► mac_traffic_totals │
│                  │     │ mac_traffic_daily  │
│                  │     │collector_checkpoints│
└──────────────────┘     └────────────────────┘
```

### 2.3 服务生命周期

```
启动阶段:
  config.Load() → Config.Validate() → Bootstrap.Validate()
  → Loader.LoadTrafficObjects() → AttachmentManager.AttachIface()
  → FlushLoop.Start() [恢复checkpoint] → HousekeepingLoop.Start()
  → HTTP Server.Start()

运行阶段:
  Flush Loop: 每 flush_interval 秒读取 eBPF map → 计算 delta → 写入 SQLite
  Housekeeping Loop: 每 housekeeping_interval 秒执行 PruneDaily(cutoff=400天前)

关闭阶段:
  收到 SIGINT/SIGTERM → context cancel
  → HousekeepingLoop.Stop() [final run] → FlushLoop.Stop() [final flush]
  → AttachmentManager.DetachAll() → 退出
```

---

## 3. 核心概念

### 3.1 服务模式

| 模式 | 条件 | 行为 |
|------|------|------|
| `healthy` | 所有配置的接口均成功附加 | 正常运行 |
| `degraded` | 部分接口附加成功，且 `allow_partial=true` | 继续运行，status 中标记失败接口 |
| `failed` | 零接口附加成功；或部分失败但 `allow_partial=false` | 启动时立即退出 |

### 3.2 Flush Loop（刷新循环）

核心职责：以固定时间间隔将 eBPF map 中的计数器增量写入 SQLite。

**关键特性**：
- **Checkpoint 恢复**：每次 flush 后保存原始计数器值到 `collector_checkpoints` 表，下次启动时从此值计算 delta，避免重复计数
- **幂等性**：即使重复调用，只要没有新流量，delta 为零，不会重复写入
- **UTC 日界**：以 UTC 0 点为界判断是否跨天，跨天时 delta = 原始计数值（而非差值）
- **Stale 检测**：若距上次成功 flush 超过 2 × flush_interval，`/healthz` 返回 503

### 3.3 Housekeeping Loop（维护循环）

核心职责：定期执行数据维护任务。

**当前唯一任务**：删除 `mac_traffic_daily` 中早于 400 天 UTC 的记录。

注意：
- `mac_traffic_totals`（全量累计表）**永远不删除**
- `collector_checkpoints` **不自动删除**（由下次 flush 时按需覆盖）

### 3.4 Mock 模式

当以下条件均满足时进入 Mock 模式：
- `clang` 不在 PATH 中
- eBPF object 文件不存在或小于 100 字节

Mock 模式下：
- 仍可执行完整的启动/停止/生命周期
- 不附加任何真实 eBPF 程序
- `/healthz` 返回 healthy，但无实际流量计数
- 用于开发测试

---

## 4. eBPF 技术详解

### 4.1 附加点: TCX (Traffic Control X)

服务使用 `TCX`（取代已废弃的 `clsact`）作为 eBPF 附加点：

| 方向 | eBPF Section | 内核 Attach Type | 抓取 MAC 来源 |
|------|--------------|-----------------|--------------|
| Ingress | `SEC("tc/ingress")` | `AttachTCXIngress` | `eth->h_dest`（目标 MAC） |
| Egress | `SEC("tc/egress")` | `AttachTCXEgress` | `eth->h_source`（源 MAC） |

> **注意**：Ingress 抓目的 MAC（谁收到了），Egress 抓源 MAC（谁发出的）。这是标准的 L2 流量监听语义。

### 4.2 eBPF Map 定义

```c
// bpf/shared.h

struct traffic_key {
    __u32 ifindex;   // 4 字节：接口索引
    __u8  mac[6];    // 6 字节：MAC 地址
};                    // 共 10 字节

struct traffic_counter {
    __u64 bytes;           // 总字节数（含 ingress + egress）
    __u64 packets;         // 总报文数（含 ingress + egress）
    __u64 ingress_bytes;   // 入站字节数
    __u64 ingress_packets; // 入站报文数
    __u64 egress_bytes;    // 出站字节数
    __u64 egress_packets;  // 出站报文数
};                        // 共 48 字节 (6 × 8)

// Map 规格
BPF_MAP_TYPE_HASH, max_entries=262144
```

### 4.3 eBPF 程序逻辑（Ingress 为例）

```c
SEC("tc/ingress")
int handle_ingress(struct __sk_buff *skb)
{
    // 1. 解析以太网头
    struct ethhdr *eth = data;
    if ((void *)(eth + 1) > data_end)
        return TC_ACT_OK;  // 数据不完整，透传

    // 2. 构造 key：{ifindex, dest_mac}
    struct traffic_key key = {
        .ifindex = skb->ifindex,
    };
    memcpy(key.mac, eth->h_dest, 6);

    // 3. 查找/创建 counter
    struct traffic_counter *counter = bpf_map_lookup_elem(&traffic_map, &key);
    if (!counter) {
        bpf_map_update_elem(&traffic_map, &key, &new_counter, BPF_ANY);
        counter = bpf_map_lookup_elem(...);
        if (!counter) return TC_ACT_OK;
    }

    // 4. 原子递增（多核安全）
    __sync_fetch_and_add(&counter->bytes, skb->len);
    __sync_fetch_and_add(&counter->packets, 1);
    __sync_fetch_and_add(&counter->ingress_bytes, skb->len);
    __sync_fetch_and_add(&counter->ingress_packets, 1);

    return TC_ACT_OK;  // 始终透传，不拦截任何数据包
}
```

### 4.4 计数器一致性

为保证多核环境下的计数一致性，使用 `__sync_fetch_and_add` 原子操作。由于 eBPF map 中的值在用户态读取时可能正处于更新中间，Flush Loop 中的 delta 计算使用**上次 checkpoint 值**作为基准，如果差值为负（计数器回绕），则丢弃该次 flush，依赖下次 flush 重新同步。

### 4.5 eBPF 对象构建

```bash
# 使用 bpf2go 从 C 源码生成 Go 绑定 + 编译 eBPF 对象
go generate ./internal/ebpf

# 产出文件:
# internal/ebpf/traffic_count_bpfel.o   # 小端 eBPF 对象
# internal/ebpf/traffic_count_bpfel.go  # Go 绑定
# internal/ebpf/traffic_count_bpfeb.o   # 大端 eBPF 对象 (可选)
# internal/ebpf/traffic_count_bpfeb.go  # Go 绑定 (可选)
```

构建依赖：
- `clang` ≥ 10
- `llvm-strip`（用于从目标文件中去除调试信息）
- Linux kernel headers（或 `bpf/bpf_helpers.h`）

---

## 5. 数据模型与存储

### 5.1 SQLite Schema

```sql
-- 全量累计表（永不删除，永不修剪）
CREATE TABLE mac_traffic_totals (
    interface_name  TEXT NOT NULL,
    ifindex         INTEGER NOT NULL,
    mac             BLOB NOT NULL,          -- 6 字节二进制
    bytes           INTEGER NOT NULL DEFAULT 0,
    packets         INTEGER NOT NULL DEFAULT 0,
    ingress_bytes   INTEGER NOT NULL DEFAULT 0,
    ingress_packets INTEGER NOT NULL DEFAULT 0,
    egress_bytes    INTEGER NOT NULL DEFAULT 0,
    egress_packets  INTEGER NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (interface_name, ifindex, mac)
) WITHOUT ROWID;

-- UTC 每日桶（400 天后由 HousekeepingLoop 删除）
CREATE TABLE mac_traffic_daily (
    interface_name  TEXT NOT NULL,
    ifindex         INTEGER NOT NULL,
    mac             BLOB NOT NULL,
    date_utc        TEXT NOT NULL,          -- "YYYY-MM-DD" UTC
    bytes           INTEGER NOT NULL DEFAULT 0,
    packets         INTEGER NOT NULL DEFAULT 0,
    ingress_bytes   INTEGER NOT NULL DEFAULT 0,
    ingress_packets INTEGER NOT NULL DEFAULT 0,
    egress_bytes    INTEGER NOT NULL DEFAULT 0,
    egress_packets  INTEGER NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (interface_name, ifindex, mac, date_utc)
) WITHOUT ROWID;

-- Checkpoint 表（用于断点恢复和 delta 计算）
CREATE TABLE collector_checkpoints (
    interface_name       TEXT NOT NULL,
    ifindex              INTEGER NOT NULL,
    mac                  BLOB NOT NULL,
    date_utc             TEXT NOT NULL,
    last_raw_bytes       INTEGER NOT NULL DEFAULT 0,  -- 上次 flush 时的原始值
    last_raw_packets     INTEGER NOT NULL DEFAULT 0,
    last_ingress_bytes   INTEGER NOT NULL DEFAULT 0,
    last_ingress_packets INTEGER NOT NULL DEFAULT 0,
    last_egress_bytes   INTEGER NOT NULL DEFAULT 0,
    last_egress_packets  INTEGER NOT NULL DEFAULT 0,
    updated_at           TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (interface_name, ifindex, mac)
) WITHOUT ROWID;

-- 索引
CREATE INDEX idx_daily_date_utc ON mac_traffic_daily(date_utc);         -- 用于修剪查询
CREATE INDEX idx_totals_interface ON mac_traffic_totals(interface_name);
CREATE INDEX idx_daily_interface ON mac_traffic_daily(interface_name);
CREATE INDEX idx_checkpoints_interface ON collector_checkpoints(interface_name);
```

### 5.2 Delta 计算逻辑

```
当 flush 被调用时，对于每条 (ifindex, mac) 记录：

if (checkpoint 存在 AND checkpoint.date_utc == 今天UTC) {
    delta = current_raw - checkpoint.last_raw  // 同一天内取差值
} else {
    delta = current_raw  // 新的一天或无 checkpoint，取原始值为 delta
}

// 如果 delta < 0（计数器回绕），则丢弃该次 flush 的写入
```

### 5.3 时间窗口查询语义

| window 参数 | SQL 语义 | 说明 |
|------------|---------|------|
| `today` | `WHERE date_utc = current_UTC_date` | 今日 0 点到现在的累计 |
| `7days` | `WHERE date_utc >= (今天 - 6天) AND date_utc <= 今天` | 含今日的近 7 天（含首尾） |
| `30days` | `WHERE date_utc >= (今天 - 29天) AND date_utc <= 今天` | 含今日的近 30 天 |
| `month` | `WHERE date_utc >= 月首 AND date_utc <= 今天` | 本月 1 日到今天 |
| `all` | 查询 `mac_traffic_totals` | 全量累计，无时间过滤 |

> **注意**：所有日期计算均使用 **UTC** 时区，不是本地时间。

---

## 6. HTTP API 参考

服务默认绑定到 `127.0.0.1:8080`（仅本地访问）。

### 6.1 `GET /healthz`

**用途**：存活探针（Kubernetes / 负载均衡健康检查）

**响应**：

| 状态码 | 条件 |
|--------|------|
| `200 OK` | 服务处于 healthy 或 degraded 模式 **且** flush loop 未 stale |
| `503 Service Unavailable` | 服务 failed 模式 **或** flush loop 超过 2× flush_interval 未执行 |

**请求示例**：
```bash
curl -v http://127.0.0.1:8080/healthz
```

### 6.2 `GET /api/v1/status`

**用途**：获取服务当前运行时状态

**响应**：
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

| 字段 | 类型 | 说明 |
|------|------|------|
| `mode` | string | `healthy` \| `degraded` \| `failed` |
| `configured_ifaces` | string[] | 配置文件中的接口列表 |
| `attached_ifaces` | string[] | 实际附加成功的接口 |
| `failed_ifaces` | string[] | 附加失败的接口 |
| `last_flush_timestamp` | int64 | Unix 时间戳，0 表示从未 flush |
| `flush_error` | string | 上次 flush 错误信息，空表示无错误 |
| `database_path` | string | SQLite 数据库文件路径 |

### 6.3 `GET /api/v1/traffic`

**用途**：查询流量记录

**查询参数**：

| 参数 | 必填 | 类型 | 默认值 | 说明 |
|------|------|------|--------|------|
| `window` | 否 | string | `today` | 时间窗口：`all`, `today`, `7days`, `30days`, `month` |
| `interface` | 否 | string | - | 按接口名过滤（精确匹配） |
| `mac` | 否 | string | - | 按 MAC 地址过滤（格式：`XX:XX:XX:XX:XX:XX`） |
| `limit` | 否 | int | 100 | 返回记录数上限，1-1000 |
| `offset` | 否 | int | 0 | 分页偏移量 |

> **注意**：对于 `window=all`，MAC 过滤在内存中应用（因为 totals 表无日期列）。对于有时间窗口的查询，MAC 过滤在数据库查询层应用。

**响应**：
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

**错误响应**：

| 状态码 | 条件 | 响应体 |
|--------|------|--------|
| `400 Bad Request` | 无效的 `window` 值 | `{"error": "invalid window, must be one of: all, 30days, 7days, today, month"}` |
| `400 Bad Request` | 无效的 MAC 格式 | `{"error": "invalid mac format, must be XX:XX:XX:XX:XX:XX"}` |
| `500 Internal Server Error` | 数据库查询错误 | `{"error": "<具体错误信息>"}` |

**请求示例**：
```bash
# 查询今日所有流量
curl "http://127.0.0.1:8080/api/v1/traffic?window=today"

# 查询近 7 天 eth0 接口的流量
curl "http://127.0.0.1:8080/api/v1/traffic?window=7days&interface=eth0"

# 查询特定 MAC 的全量累计
curl "http://127.0.0.1:8080/api/v1/traffic?window=all&mac=aa:bb:cc:dd:ee:ff"

# 分页查询
curl "http://127.0.0.1:8080/api/v1/traffic?limit=10&offset=20"
```

---

## 7. 配置指南

### 7.1 配置文件格式

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

### 7.2 配置字段详解

| 字段 | 必填 | 类型 | 默认值 | 说明 |
|------|------|------|--------|------|
| `interfaces` | **是** | string[] | - | 允许附加 eBPF 程序的接口名列表（白名单）。不支持通配符。 |
| `bind_address` | **是** | string | - | HTTP API 监听地址。**必须**为回环地址（`127.0.0.1`、`localhost` 或 `::1`），不允许监听公网。 |
| `database_path` | **是** | string | - | SQLite 数据库文件路径。父目录必须存在且对运行用户可写。 |
| `flush_interval` | **是** | int > 0 | - | Flush loop 间隔秒数。建议值：5-60 秒。过短会增加 SQLite 写入压力，过长可能丢失较多计数。 |
| `housekeeping_interval` | 否 | int > 0 | 3600 | Housekeeping loop 间隔秒数。默认 1 小时。 |
| `log_level` | 否 | string | `info` | 日志级别：`debug`、`info`、`warn`、`error` |
| `allow_partial` | 否 | bool | `false` | 部分接口附加失败时是否允许服务继续运行（degraded 模式）。 |

### 7.3 验证规则

启动时配置会经过严格验证：

| 字段 | 验证规则 |
|------|---------|
| `interfaces` | 至少配置一个接口；接口名不能为空字符串 |
| `bind_address` | 必须可解析为 host:port；host 必须是回环地址；端口 1-65535 |
| `database_path` | 不能为空字符串 |
| `flush_interval` | 必须 > 0 |
| `log_level` | 必须是 `debug`、`info`、`warn`、`error` 之一 |

### 7.4 典型部署配置

**最小化生产配置**（`/etc/traffic-count/config.json`）：
```json
{
  "interfaces": ["eth0"],
  "bind_address": "127.0.0.1:8080",
  "database_path": "/var/lib/traffic-count/traffic-count.db",
  "flush_interval": 10,
  "log_level": "info"
}
```

**多接口配置**：
```json
{
  "interfaces": ["eth0", "wg0", "wg1"],
  "bind_address": "127.0.0.1:8080",
  "database_path": "/var/lib/traffic-count/traffic-count.db",
  "flush_interval": 30,
  "housekeeping_interval": 7200,
  "log_level": "warn",
  "allow_partial": true
}
```

**本地测试配置**（使用 loopback 接口，无需 root）：
```json
{
  "interfaces": ["lo"],
  "bind_address": "127.0.0.1:8080",
  "database_path": "./traffic-count.db",
  "flush_interval": 5,
  "log_level": "debug"
}
```

---

## 8. 部署指南

### 8.1 系统要求

| 要求 | 最小版本 | 说明 |
|------|---------|------|
| Linux Kernel | ≥ 5.8 | TCX 附加点需要较新内核 |
| Go | ≥ 1.21 | 服务运行时语言 |
| clang | ≥ 10 | eBPF 目标文件编译（Mock 模式可不安装） |
| root 权限 | - | eBPF 程序附加和 TC qdisc 检查需要 CAP_SYS_ADMIN、CAPE_NET_ADMIN |

> **提示**：使用 `uname -r` 检查内核版本。使用 `clang --version` 检查 clang 版本。

### 8.2 构建

```bash
# 克隆代码
git clone git@github.com:KexiChanProjectProxy/wlt-traffic.git
cd wlt-traffic

# 构建（需要 clang）
make build

# 输出：
#   bin/traffic_count           # Go 可执行文件
#   bin/traffic_count_bpfel.o  # eBPF 目标文件

# 仅构建 Go 二进制（不重新编译 eBPF）
make build-binary

# 清理构建产物
make clean
```

### 8.3 数据库目录准备

```bash
# 创建数据库目录
sudo mkdir -p /var/lib/traffic-count
sudo chown traffic-count:traffic-count /var/lib/traffic-count
sudo chmod 700 /var/lib/traffic-count

# 创建配置目录
sudo mkdir -p /etc/traffic-count
sudo chown root:root /etc/traffic-count
sudo chmod 755 /etc/traffic-count
```

### 8.4 systemd 部署

**步骤 1**：创建专用用户（可选但推荐）

```bash
sudo useradd -r -s /sbin/nologin traffic-count
```

**步骤 2**：复制二进制和配置

```bash
sudo cp bin/traffic-count /usr/local/bin/
sudo cp examples/config.json /etc/traffic-count/config.json
sudo cp examples/traffic-count.service /etc/systemd/system/traffic-count.service
```

**步骤 3**：编辑 `/etc/traffic-count/config.json`

```bash
sudo $EDITOR /etc/traffic-count/config.json
```

**步骤 4**：配置 systemd 服务文件权限

```bash
sudo chown root:root /etc/systemd/system/traffic-count.service
sudo chmod 644 /etc/systemd/system/traffic-count.service
```

**步骤 5**：启动服务

```bash
sudo systemctl daemon-reload
sudo systemctl enable traffic-count
sudo systemctl start traffic-count

# 检查状态
sudo systemctl status traffic-count

# 查看日志
sudo journalctl -u traffic-count -f
```

### 8.5 直接运行（非 systemd）

```bash
# 需要 root 权限
sudo ./bin/traffic-count --config examples/config.json

# 或使用 loopback 测试（可选，不需要 root，但需要调整 config.json）
./bin/traffic-count --config examples/config-loopback.json
```

---

## 9. 运行与维护

### 9.1 启动检查清单

| 检查项 | 命令 | 期望结果 |
|--------|------|---------|
| 服务运行 | `systemctl status traffic-count` | `active (running)` |
| HTTP 响应 | `curl http://127.0.0.1:8080/healthz` | `200 OK` |
| 状态接口 | `curl http://127.0.0.1:8080/api/v1/status` | JSON with `mode: healthy` |
| 内核日志 | `dmesg \| grep -i traffic` | 无 eBPF 附加错误 |

### 9.2 数据库维护

**手动清理历史数据**（不推荐，仅在紧急情况下使用）：

```bash
# 进入 SQLite
sqlite3 /var/lib/traffic-count/traffic-count.db

# 查看表大小
sqlite> SELECT COUNT(*) FROM mac_traffic_daily;
sqlite> SELECT COUNT(*) FROM mac_traffic_totals;

# 手动删除 400 天前的数据
sqlite> DELETE FROM mac_traffic_daily WHERE date_utc < date('now', '-400 days');
sqlite> VACUUM;  -- 回收磁盘空间

# 验证删除
sqlite> SELECT COUNT(*) FROM mac_traffic_daily;
```

### 9.3 日志级别调整

通过重启服务修改 `log_level`，支持动态调整：
```bash
# 编辑配置
sudo $EDITOR /etc/traffic-count/config.json
# 将 "log_level": "info" 改为 "debug"

# 重启
sudo systemctl restart traffic-count
```

### 9.4 滚动更新

1. 编译新版本二进制
2. 使用 `systemctl stop traffic-count` 停止服务
3. 替换 `/usr/local/bin/traffic-count`
4. 使用 `systemctl start traffic-count` 启动服务

> **注意**：停止服务时会执行最后一次 flush（将内存中的计数器写入 SQLite），确保不丢失数据。

### 9.5 备份

```bash
# 备份数据库文件（建议在低峰期执行，此时 flush 已完成）
sudo systemctl stop traffic-count
sudo cp /var/lib/traffic-count/traffic-count.db /var/lib/traffic-count/traffic-count.db.bak.$(date +%Y%m%d)
sudo systemctl start traffic-count
```

---

## 10. 故障排查

### 10.1 常见错误及解决方案

#### 错误 1：启动失败 - "root privileges required"

```
error: startup validation failed: root privileges required (CAP_SYS_ADMIN, CAP_NET_ADMIN)
```

**原因**：未以 root 身份运行。

**解决**：
```bash
sudo ./bin/traffic-count --config /etc/traffic-count/config.json
```

---

#### 错误 2：接口附加失败 - "interface does not exist"

```
failed interfaces: [eth99]
startup result: {"mode":"failed",...}
```

**原因**：配置的接口名在系统中不存在。

**解决**：
1. 确认接口名：`ip link show`
2. 修正配置中的接口名：`"interfaces": ["eth0", "wg0"]`

---

#### 错误 3：Flush 循环 stale - `/healthz` 返回 503

```
// status 中显示：
"last_flush_timestamp": 1744567800,  // 很久以前的时间戳
"flush_error": ""
```

**原因**：
- Flush 循环未能在 2× flush_interval 内成功执行
- 可能原因：SQLite 写入阻塞、数据库文件损坏、磁盘满

**排查**：
```bash
# 检查数据库文件权限
ls -la /var/lib/traffic-count/

# 检查磁盘空间
df -h /var/lib/traffic-count/

# 检查 SQLite 完整性
sqlite3 /var/lib/traffic-count/traffic-count.db "PRAGMA integrity_check;"

# 查看 flush 错误
curl http://127.0.0.1:8080/api/v1/status | jq '.flush_error'
```

---

#### 错误 4：Bind address 验证失败

```
config validation failed: bind address must be loopback (127.0.0.1 or localhost or ::1), got "0.0.0.0"
```

**原因**：`bind_address` 不允许监听所有接口（安全设计）。

**解决**：修改为回环地址：
```json
"bind_address": "127.0.0.1:8080"
```

---

#### 错误 5：Mock 模式运行（无真实计数）

```
// status 中无 attached_ifaces 或 mode 为 failed
// 但服务未退出（因为 allow_partial 可能为 true）
```

**原因**：
- clang 不在 PATH 中
- eBPF 对象文件不存在或损坏

**解决**：
```bash
# 确认 clang 是否安装
clang --version

# 重新编译 eBPF 对象
make build

# 确认对象文件存在
ls -la bin/traffic_count_bpfel.o
```

---

#### 错误 6：重复计数（delta 过大）

**表现**：某次查询发现计数器异常增大。

**原因**：服务在 flush 过程中异常退出，导致 checkpoint 未更新；重启后 delta 计算使用了旧的 checkpoint 基准。

**解决**：这是已知的设计权衡（可靠性 > 精确性）。下次 flush 会恢复正常。可以通过手动检查 `collector_checkpoints` 表对比 `mac_traffic_daily` 的 `updated_at` 时间戳来确认。

---

### 10.2 调试模式

启用 `debug` 日志级别查看详细运行信息：

```json
{
  "log_level": "debug"
}
```

重启后查看 journal 日志：
```bash
sudo journalctl -u traffic-count -f --priority debug
```

### 10.3 eBPF 验证

确认 eBPF 程序已正确加载：
```bash
# 查看加载的 eBPF 程序
bpftool prog list

# 查看 traffic_map
bpftool map list

# 查看特定 map 的内容（需要 root）
bpftool map dump id <map_id>
```

### 10.4 网络 namespace 注意事项

如果在 network namespace 内运行（如 Docker/容器），确保：
1. 容器以 `--privileged` 模式运行
2. 挂载了 `/sys/fs/bpf`
3. 目标网络接口在容器可见的 namespace 中

**不推荐**在容器内运行此服务，推荐以 host 网络模式运行或直接在宿主机部署。

---

## 附录

### A. Makefile 目标

| 目标 | 说明 |
|------|------|
| `make build` | 构建 eBPF 对象 + Go 二进制 |
| `make build-ebpf` | 仅生成 eBPF Go 绑定 |
| `make build-binary` | 仅构建 Go 二进制 |
| `make test` | 运行所有测试 (`go test ./...`) |
| `make clean` | 清理构建产物 |
| `make run` | 构建并运行 |
| `make verify-layout` | 打印 eBPF key/value 布局信息 |

### B. 目录结构

```
traffic-count/
├── cmd/traffic-count/main.go          # 程序入口
├── internal/
│   ├── bootstrap/bootstrap.go          # 启动验证
│   ├── config/config.go                # 配置加载与验证
│   ├── ebpf/
│   │   ├── ebpf.go                    # eBPF 通用定义
│   │   ├── loader.go                  # eBPF 对象加载
│   │   ├── attachment.go              # TCX 附加管理
│   │   ├── map.go                     # traffic_map 封装
│   │   └── *_bpfel.go                 # bpf2go 生成的绑定
│   ├── http/http.go                    # HTTP API 服务器
│   ├── runtime/
│   │   ├── runtime.go                 # Service 生命周期管理
│   │   ├── flush.go                   # Flush Loop
│   │   └── housekeeping.go            # Housekeeping Loop
│   └── storage/storage.go              # SQLite 仓库
├── bpf/
│   ├── shared.h                        # eBPF 共享结构体
│   ├── ingress.c                       # Ingress eBPF 程序
│   └── egress.c                        # Egress eBPF 程序
├── examples/
│   ├── config.json                     # 生产配置示例
│   ├── config-loopback.json            # 本地测试配置
│   └── traffic-count.service           # systemd 服务文件
├── bin/                                # 构建输出目录
├── go.mod / go.sum                     # Go 依赖
├── Makefile                            # 构建脚本
└── README.md                            # 项目自述
```

### C. 关键设计决策记录

| 决策 | 理由 |
|------|------|
| TCX 而非 clsact | clsact 已在内核 5.17 中移除，TCX 是其替代方案 |
| WITHOUT ROWID 表 | SQLite 推荐的高性能模式，减少存储空间和 I/O |
| WAL 模式 | 允许并发读取，写入不阻塞读取 |
| UTC 日界 | 避免时区转换歧义，简化跨天计算 |
| 400 天保留期 | 平衡存储空间和历史数据需求 |
| Loopback 地址绑定 | 防止 API 被公网访问（安全设计） |
| Checkpoint 持久化 | 确保服务崩溃/重启后不重复计数 |

---

*文档版本：1.0 | 最后更新：2026-04-14*
