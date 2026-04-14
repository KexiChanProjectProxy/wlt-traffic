# traffic-count HTTP API 参考文档

> 本文档为 Machine-readable / LLM-friendly 格式，可直接被 AI 工具解析和使用。

---

## 基本信息

| 属性 | 值 |
|------|-----|
| **Base URL** | `http://127.0.0.1:8080` |
| **协议** | HTTP/1.1 |
| **认证** | 无（服务仅监听本地回环地址） |
| **数据格式** | JSON (`Content-Type: application/json`) |
| **字符编码** | UTF-8 |

> ⚠️ **安全约束**：服务强制要求 `bind_address` 为回环地址（`127.0.0.1`、`localhost` 或 `::1`），不允许公网访问。

---

## 端点概览

| 方法 | 路径 | 描述 | 认证 |
|------|------|------|------|
| `GET` | `/healthz` | 存活探针 | 无 |
| `GET` | `/api/v1/status` | 服务状态 | 无 |
| `GET` | `/api/v1/traffic` | 流量记录查询 | 无 |

---

## `GET /healthz`

### 描述

存活探针（liveness probe），用于 Kubernetes、负载均衡器或监控系统检查服务是否处于可用状态。

### 请求

```
GET /healthz
Host: 127.0.0.1:8080
```

无请求参数。

### 响应

#### 200 OK — 服务健康

服务处于 `healthy` 或 `degraded` 模式，且 flush loop 未过期（距离上次 flush 未超过 2× flush_interval）。

```
HTTP/1.1 200 OK
Content-Type: text/plain
(空响应体)
```

#### 503 Service Unavailable — 服务不健康

服务处于 `failed` 模式，或 flush loop 处于 stale 状态（超过 2× flush_interval 未执行）。

```
HTTP/1.1 503 Service Unavailable
Content-Type: text/plain
(空响应体)
```

### 判断逻辑（伪代码）

```
IF (service.mode IN [healthy, degraded] AND NOT flush_loop.is_stale()):
    RETURN 200
ELSE:
    RETURN 503
```

### 用途

- Kubernetes livenessProbe
- systemd service health check
- 负载均衡器健康检查
- 监控告警触发

---

## `GET /api/v1/status`

### 描述

返回服务的当前运行时状态，包括接口附加情况、flush 时间戳、错误信息等。

### 请求

```
GET /api/v1/status
Host: 127.0.0.1:8080
```

无请求参数。

### 响应

#### 200 OK

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

#### 响应字段说明

| 字段名 | 类型 | 始终存在 | 说明 |
|--------|------|---------|------|
| `mode` | `string` | ✅ | 服务运行模式：`"healthy"` \| `"degraded"` \| `"failed"` |
| `configured_ifaces` | `string[]` | ✅ | 配置文件 `interfaces` 中声明的所有接口 |
| `attached_ifaces` | `string[]` | ✅ | eBPF 程序实际附加成功的接口列表 |
| `failed_ifaces` | `string[]` | ✅ | eBPF 程序附加失败的接口列表 |
| `last_flush_timestamp` | `integer` | ✅ | 上次成功 flush 的 Unix 时间戳（秒），0 表示从未成功 flush |
| `flush_error` | `string` | ✅ | 上次 flush 错误信息，空字符串表示无错误 |
| `database_path` | `string` | ✅ | SQLite 数据库文件路径 |

### 模式说明

| mode | 条件 |
|------|------|
| `healthy` | 所有配置的接口均成功附加 |
| `degraded` | 部分接口成功，部分失败，且 `allow_partial=true` |
| `failed` | 零接口成功附加；或部分失败且 `allow_partial=false` |

### 示例

**请求：**
```bash
curl -s http://127.0.0.1:8080/api/v1/status | jq .
```

**响应（healthy 模式）：**
```json
{
  "mode": "healthy",
  "configured_ifaces": [
    "eth0",
    "wg0"
  ],
  "attached_ifaces": [
    "eth0",
    "wg0"
  ],
  "failed_ifaces": [],
  "last_flush_timestamp": 1744567800,
  "flush_error": "",
  "database_path": "/var/lib/traffic-count/traffic-count.db"
}
```

**响应（degraded 模式）：**
```json
{
  "mode": "degraded",
  "configured_ifaces": [
    "eth0",
    "wg0",
    "wg1"
  ],
  "attached_ifaces": [
    "eth0",
    "wg0"
  ],
  "failed_ifaces": [
    "wg1"
  ],
  "last_flush_timestamp": 1744567800,
  "flush_error": "",
  "database_path": "/var/lib/traffic-count/traffic-count.db"
}
```

---

## `GET /api/v1/traffic`

### 描述

查询流量记录，支持按时间窗口、接口、MAC 地址过滤，支持分页。

### 请求

```
GET /api/v1/traffic?window=today&interface=eth0&mac=aa:bb:cc:dd:ee:ff&limit=100&offset=0
Host: 127.0.0.1:8080
```

### 查询参数

| 参数名 | 类型 | 必填 | 默认值 | 可选值 | 说明 |
|--------|------|------|--------|--------|------|
| `window` | `string` | 否 | `"today"` | `"all"`, `"today"`, `"7days"`, `"30days"`, `"month"` | 时间窗口 |
| `interface` | `string` | 否 | 无（不过滤） | 任意字符串 | 按接口名精确过滤 |
| `mac` | `string` | 否 | 无（不过滤） | `XX:XX:XX:XX:XX:XX`（大写或小写） | 按 MAC 地址过滤（必须符合格式） |
| `limit` | `integer` | 否 | `100` | 1–1000 | 返回记录数上限 |
| `offset` | `integer` | 否 | `0` | ≥ 0 | 分页偏移量 |

#### `window` 参数语义

| 值 | SQL 条件 | 时间范围 | 数据来源表 |
|----|---------|---------|-----------|
| `"today"` | `date_utc = current_UTC_date` | 今日 0:00 UTC 至今 | `mac_traffic_daily` |
| `"7days"` | `date_utc >= (今天 - 6天) AND date_utc <= 今天` | 近 7 天（含今日） | `mac_traffic_daily` |
| `"30days"` | `date_utc >= (今天 - 29天) AND date_utc <= 今天` | 近 30 天（含今日） | `mac_traffic_daily` |
| `"month"` | `date_utc >= 月首 AND date_utc <= 今天` | 本月 1 日至今 | `mac_traffic_daily` |
| `"all"` | 无时间条件 | 全部历史 | `mac_traffic_totals` |

> 📌 所有日期计算使用 **UTC** 时区。

### 响应

#### 200 OK

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

#### 响应字段说明

| 字段名 | 类型 | 说明 |
|--------|------|------|
| `window` | `string` | 请求的窗口值（echo back） |
| `limit` | `integer` | 实际应用的 limit 值（echo back） |
| `offset` | `integer` | 实际应用的 offset 值（echo back） |
| `records` | `TrafficRecord[]` | 流量记录数组，空数组表示无数据 |

##### TrafficRecord

| 字段名 | 类型 | 说明 |
|--------|------|------|
| `interface` | `string` | 网络接口名称 |
| `ifindex` | `integer` | 内核接口索引编号 |
| `mac` | `string` | MAC 地址（格式：`XX:XX:XX:XX:XX:XX`） |
| `ingress_bytes` | `integer` | 入站字节数（仅该接口接收方向） |
| `egress_bytes` | `integer` | 出站字节数（仅该接口发送方向） |
| `total_bytes` | `integer` | 总字节数（ingress_bytes + egress_bytes） |
| `ingress_packets` | `integer` | 入站报文数 |
| `egress_packets` | `integer` | 出站报文数 |
| `total_packets` | `integer` | 总报文数（ingress_packets + egress_packets） |
| `window` | `string` | 该条记录所属的窗口（与顶层 `window` 相同） |

#### 400 Bad Request — 参数错误

**无效的 window 值：**
```json
{
  "error": "invalid window, must be one of: all, 30days, 7days, today, month"
}
```

**无效的 MAC 格式：**
```json
{
  "error": "invalid mac format, must be XX:XX:XX:XX:XX:XX"
}
```

#### 500 Internal Server Error — 数据库错误

```json
{
  "error": "listing daily records: database is locked"
}
```

### 示例

#### 示例 1：查询今日所有流量（默认）

**请求：**
```bash
curl -s "http://127.0.0.1:8080/api/v1/traffic" | jq .
```

**响应：**
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
      "ingress_bytes": 102400,
      "egress_bytes": 51200,
      "total_bytes": 153600,
      "ingress_packets": 100,
      "egress_packets": 50,
      "total_packets": 150,
      "window": "today"
    },
    {
      "interface": "wg0",
      "ifindex": 3,
      "mac": "11:22:33:44:55:66",
      "ingress_bytes": 204800,
      "egress_bytes": 102400,
      "total_bytes": 307200,
      "ingress_packets": 200,
      "egress_packets": 100,
      "total_packets": 300,
      "window": "today"
    }
  ]
}
```

#### 示例 2：查询特定接口近 7 天流量

**请求：**
```bash
curl -s "http://127.0.0.1:8080/api/v1/traffic?window=7days&interface=eth0" | jq .
```

**响应：**
```json
{
  "window": "7days",
  "limit": 100,
  "offset": 0,
  "records": [
    {
      "interface": "eth0",
      "ifindex": 2,
      "mac": "aa:bb:cc:dd:ee:ff",
      "ingress_bytes": 716800,
      "egress_bytes": 358400,
      "total_bytes": 1075200,
      "ingress_packets": 700,
      "egress_packets": 350,
      "total_packets": 1050,
      "window": "7days"
    }
  ]
}
```

#### 示例 3：查询特定 MAC 的全量累计

**请求：**
```bash
curl -s "http://127.0.0.1:8080/api/v1/traffic?window=all&mac=aa:bb:cc:dd:ee:ff" | jq .
```

**响应：**
```json
{
  "window": "all",
  "limit": 100,
  "offset": 0,
  "records": [
    {
      "interface": "eth0",
      "ifindex": 2,
      "mac": "aa:bb:cc:dd:ee:ff",
      "ingress_bytes": 10737418240,
      "egress_bytes": 5368709120,
      "total_bytes": 16106127360,
      "ingress_packets": 10485760,
      "egress_packets": 5242880,
      "total_packets": 15728640,
      "window": "all"
    }
  ]
}
```

#### 示例 4：分页查询

**请求（第一页）：**
```bash
curl -s "http://127.0.0.1:8080/api/v1/traffic?window=30days&limit=10&offset=0" | jq .
```

**请求（第二页）：**
```bash
curl -s "http://127.0.0.1:8080/api/v1/traffic?window=30days&limit=10&offset=10" | jq .
```

#### 示例 5：查询本月流量

**请求：**
```bash
curl -s "http://127.0.0.1:8080/api/v1/traffic?window=month" | jq .
```

#### 示例 6：完整 curl 请求示例

```bash
# 健康检查
curl -w "\nHTTP Status: %{http_code}\n" http://127.0.0.1:8080/healthz

# 服务状态
curl -s http://127.0.0.1:8080/api/v1/status | jq .

# 今日流量（前 50 条）
curl -s "http://127.0.0.1:8080/api/v1/traffic?window=today&limit=50" | jq .

# 特定接口 + MAC 过滤
curl -s "http://127.0.0.1:8080/api/v1/traffic?window=7days&interface=wg0&mac=11:22:33:44:55:66" | jq .
```

---

## 错误响应格式

所有错误响应均遵循以下 JSON 格式：

```json
{
  "error": "<错误描述字符串>"
}
```

| HTTP 状态码 | 触发条件 | `error` 示例 |
|-----------|---------|-------------|
| `400` | `window` 参数值非法 | `"invalid window, must be one of: all, 30days, 7days, today, month"` |
| `400` | `mac` 参数格式非法 | `"invalid mac format, must be XX:XX:XX:XX:XX:XX"` |
| `500` | 数据库查询失败 | `"listing daily records: database is locked"` |

---

## 数据类型定义

### MAC 地址格式

标准 IEEE 802 格式：`XX:XX:XX:XX:XX:XX`，十六进制大写或小写。

**有效示例：**
- `AA:BB:CC:DD:EE:FF`
- `aa:bb:cc:dd:ee:ff`
- `Aa:Bb:Cc:Dd:Ee:Ff`

**无效示例：**
- `AA-BB-CC-DD-EE-FF`（使用 `-` 分隔符）
- `AABBCCDDEEFF`（无分隔符）
- `aa:bb:cc:dd:ee`（不足 6 字节）
- `gg:hh:ii:jj:kk:ll`（包含非十六进制字符）

### 时间戳

Unix 时间戳（秒），UTC 时区。例如：`1744567800` 对应 `2026-04-13 15:30:00 UTC`。

### 时间窗口日期计算示例（假设当前 UTC 时间为 `2026-04-14`）

| window | date_start | date_end | 说明 |
|--------|-----------|---------|------|
| `today` | `2026-04-14` | `2026-04-14` | 今日 |
| `7days` | `2026-04-08` | `2026-04-14` | 包含首尾 |
| `30days` | `2026-03-16` | `2026-04-14` | 包含首尾 |
| `month` | `2026-04-01` | `2026-04-14` | 本月至今 |

---

## 速率限制

服务不实施任何速率限制。但由于是本地部署（仅监听 127.0.0.1），实际访问受本地网络安全策略约束。

---

## OpenAPI 3.0 规格（YAML）

```yaml
openapi: 3.0.3
info:
  title: traffic-count API
  description: MAC-level traffic accounting service HTTP API
  version: 1.0.0
servers:
  - url: http://127.0.0.1:8080
    description: Local traffic-count server

paths:
  /healthz:
    get:
      summary: Liveness probe
      description: Returns 200 if service is healthy and flush loop is not stale, 503 otherwise.
      operationId: healthz
      tags:
        - Health
      responses:
        '200':
          description: Service is healthy
          content:
            text/plain:
              schema:
                type: string
                example: ""
        '503':
          description: Service is unavailable (failed mode or stale flush)
          content:
            text/plain:
              schema:
                type: string
                example: ""

  /api/v1/status:
    get:
      summary: Service status
      description: Returns current runtime status including interface attachment state and flush information.
      operationId: getStatus
      tags:
        - Status
      responses:
        '200':
          description: Service status
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/StatusResponse'
              example:
                mode: healthy
                configured_ifaces: ["eth0", "wg0"]
                attached_ifaces: ["eth0", "wg0"]
                failed_ifaces: []
                last_flush_timestamp: 1744567800
                flush_error: ""
                database_path: /var/lib/traffic-count/traffic-count.db

  /api/v1/traffic:
    get:
      summary: Query traffic records
      description: Query MAC traffic records with optional filtering by time window, interface, and MAC address.
      operationId: getTraffic
      tags:
        - Traffic
      parameters:
        - name: window
          in: query
          description: Time window for the query
          required: false
          schema:
            type: string
            enum: [all, today, 7days, 30days, month]
            default: today
        - name: interface
          in: query
          description: Filter by network interface name (exact match)
          required: false
          schema:
            type: string
            example: eth0
        - name: mac
          in: query
          description: Filter by MAC address (format XX:XX:XX:XX:XX:XX)
          required: false
          schema:
            type: string
            pattern: '^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$'
            example: aa:bb:cc:dd:ee:ff
        - name: limit
          in: query
          description: Maximum number of records to return (1-1000)
          required: false
          schema:
            type: integer
            minimum: 1
            maximum: 1000
            default: 100
        - name: offset
          in: query
          description: Pagination offset (>= 0)
          required: false
          schema:
            type: integer
            minimum: 0
            default: 0
      responses:
        '200':
          description: Traffic records
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/TrafficResponse'
        '400':
          description: Invalid parameters
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/ErrorResponse'
              examples:
                invalid_window:
                  summary: Invalid window parameter
                  value:
                    error: "invalid window, must be one of: all, 30days, 7days, today, month"
                invalid_mac:
                  summary: Invalid MAC format
                  value:
                    error: "invalid mac format, must be XX:XX:XX:XX:XX:XX"
        '500':
          description: Database error
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/ErrorResponse'
              example:
                error: "listing daily records: database is locked"

components:
  schemas:
    StatusResponse:
      type: object
      required:
        - mode
        - configured_ifaces
        - attached_ifaces
        - failed_ifaces
        - last_flush_timestamp
        - flush_error
        - database_path
      properties:
        mode:
          type: string
          enum: [healthy, degraded, failed]
          description: Current service mode
        configured_ifaces:
          type: array
          items:
            type: string
          description: Interfaces declared in configuration
        attached_ifaces:
          type: array
          items:
            type: string
          description: Interfaces with successfully attached eBPF programs
        failed_ifaces:
          type: array
          items:
            type: string
          description: Interfaces that failed to attach
        last_flush_timestamp:
          type: integer
          format: int64
          description: Unix timestamp of last successful flush (0 if never flushed)
        flush_error:
          type: string
          description: Last flush error message (empty string if no error)
        database_path:
          type: string
          description: SQLite database file path

    TrafficResponse:
      type: object
      required:
        - window
        - limit
        - offset
        - records
      properties:
        window:
          type: string
          description: Requested time window (echo back)
        limit:
          type: integer
          description: Applied limit (echo back)
        offset:
          type: integer
          description: Applied offset (echo back)
        records:
          type: array
          items:
            $ref: '#/components/schemas/TrafficRecord'
          description: Array of traffic records (may be empty)

    TrafficRecord:
      type: object
      required:
        - interface
        - ifindex
        - mac
        - ingress_bytes
        - egress_bytes
        - total_bytes
        - ingress_packets
        - egress_packets
        - total_packets
        - window
      properties:
        interface:
          type: string
          description: Network interface name
          example: eth0
        ifindex:
          type: integer
          format: uint32
          description: Kernel interface index
          example: 2
        mac:
          type: string
          pattern: '^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$'
          description: MAC address (format XX:XX:XX:XX:XX:XX)
          example: aa:bb:cc:dd:ee:ff
        ingress_bytes:
          type: integer
          format: uint64
          description: Inbound byte count
          example: 102400
        egress_bytes:
          type: integer
          format: uint64
          description: Outbound byte count
          example: 51200
        total_bytes:
          type: integer
          format: uint64
          description: Total bytes (ingress_bytes + egress_bytes)
          example: 153600
        ingress_packets:
          type: integer
          format: uint64
          description: Inbound packet count
          example: 100
        egress_packets:
          type: integer
          format: uint64
          description: Outbound packet count
          example: 50
        total_packets:
          type: integer
          format: uint64
          description: Total packets (ingress_packets + egress_packets)
          example: 150
        window:
          type: string
          description: Time window this record belongs to
          example: today

    ErrorResponse:
      type: object
      required:
        - error
      properties:
        error:
          type: string
          description: Error description
          example: "invalid window, must be one of: all, 30days, 7days, today, month"
```

---

## 测试用例参考

### TC1: 健康检查 - 服务健康

```bash
# 前提条件：服务正常运行，无附加错误，flush 未过期
curl -w "\n%{http_code}\n" http://127.0.0.1:8080/healthz
# 期望：输出空，HTTP 200
```

### TC2: 健康检查 - 服务失败

```bash
# 前提条件：服务处于 failed 模式
curl -w "\n%{http_code}\n" http://127.0.0.1:8080/healthz
# 期望：HTTP 503
```

### TC3: 状态查询 - 正常响应

```bash
curl -s http://127.0.0.1:8080/api/v1/status | jq '.mode'
# 期望：输出 "healthy" 或 "degraded" 或 "failed"
```

### TC4: 流量查询 - 默认参数

```bash
curl -s "http://127.0.0.1:8080/api/v1/traffic" | jq '.window, .limit, .offset'
# 期望：today, 100, 0
```

### TC5: 流量查询 - 全量 + MAC 过滤

```bash
curl -s "http://127.0.0.1:8080/api/v1/traffic?window=all&mac=aa:bb:cc:dd:ee:ff" | jq '.records | length'
# 期望：返回该 MAC 的全量记录数
```

### TC6: 流量查询 - 无效 window

```bash
curl -s "http://127.0.0.1:8080/api/v1/traffic?window=invalid" | jq '.error'
# 期望：包含 "invalid window" 的错误信息
```

### TC7: 流量查询 - 无效 MAC 格式

```bash
curl -s "http://127.0.0.1:8080/api/v1/traffic?mac=invalid" | jq '.error'
# 期望：包含 "invalid mac format" 的错误信息
```

### TC8: 流量查询 - 分页

```bash
# 获取总数
total=$(curl -s "http://127.0.0.1:8080/api/v1/traffic?window=30days&limit=1000" | jq '.records | length')
# 验证分页
curl -s "http://127.0.0.1:8080/api/v1/traffic?window=30days&limit=10&offset=0" | jq '.records | length'
# 期望：<= 10
```

---

*API 版本：v1 | 文档版本：1.0.0*
