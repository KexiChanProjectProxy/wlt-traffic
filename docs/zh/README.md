# traffic-count（wlt-traffic）中文说明

基于 eBPF 的局域网**按设备**流量统计服务，是 [wlt](https://github.com/KexiChanProjectProxy/wlt) 的流量后端：wlt 用它给每台设备展示自己的用量，并给管理员展示排行。

## 统计口径

在每个配置的 LAN 接口（一般就是 LAN 网桥，如 `br-lan`）上挂两个 tc 程序。上下行一律以**设备视角**定义：

| LAN 接口上的钩子 | 帧方向 | 用哪个 MAC 识别设备 | 计为 |
|---|---|---|---|
| ingress | 设备 → 路由器 | 以太网**源** MAC | 上传（upload） |
| egress  | 路由器 → 设备 | 以太网**目的** MAC | 下载（download） |

- 组播/广播帧不归属任何设备，不计入。
- 字节数为 `skb->len`（二层帧，不含 FCS）；包数按 GSO 分段数计。
- 计数器为 per-CPU 的 LRU 哈希表，键为 `(ifindex, MAC)`，键的填充字节显式清零。
- 程序返回 `TC_ACT_UNSPEC` / `TCX_NEXT`，不会截断其他 TCX 程序或传统 tc 过滤器（SQM、ifb 重定向等）。
- 内核 ≥ 6.6 使用 TCX，更老的内核自动回退到 clsact + cls_bpf。
- 配置的接口被删除重建（网桥经常如此）后会自动重新挂载。

服务每隔 `flush_interval` 秒读取一次计数，减去上一次读数（基线保存在内存中——内核 map 与进程同生命周期），把增量按**本地时区**的日期写入 SQLite。写入失败会保留增量并在下次重试；退出时会做最后一次落盘。

只配置网桥本身，不要同时配置它的成员口，否则桥接的帧会被计两次。WireGuard、tun 等三层接口没有 MAC，会被拒绝。

## 构建

```sh
make build      # 生成 bin/traffic-count，BPF 对象已内嵌
make test
make e2e        # 需要 root：在网络命名空间中做真实内核测试（TCX 与 tc 两种模式）
make generate   # 仅在修改 bpf/traffic.c 后需要；依赖 clang + libbpf-dev
make deb        # 生成本机架构的 ../traffic-count_<版本>_<架构>.deb
```

## 配置

`/etc/traffic-count/config.json`：

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

| 字段 | 默认值 | 含义 |
|---|---|---|
| `interfaces` | 必填 | 要统计的面向局域网的二层接口 |
| `bind_address` | `127.0.0.1:8081` | API 监听地址，必须是回环地址 |
| `database_path` | `/var/lib/traffic-count/traffic-count.db` | SQLite 文件 |
| `flush_interval` | `10` | 采样/落盘间隔（秒） |
| `housekeeping_interval` | `3600` | 清理过期日数据的间隔（秒） |
| `retention_days` | `400` | 日数据保留天数；累计总量永久保留 |
| `timezone` | `Local` | IANA 时区名（如 `Asia/Shanghai`），决定"今天"从几点开始 |
| `allow_partial` | `false` | 部分接口不存在时也启动（接口出现后自动挂载） |

未知字段会报错，避免拼写错误被静默忽略。

## API

均返回 JSON。`upload` 为设备发出，`download` 为设备接收。时间窗口：`today`、`yesterday`、`7days`、`30days`、`month`、`lastmonth`、`all`。

| 接口 | 用途 |
|---|---|
| `GET /healthz` | 所有接口已挂载且落盘正常时返回 200 |
| `GET /api/v1/status` | 接口状态（`tcx`/`tc`、错误）、最后落盘时间、时区 |
| `GET /api/v1/traffic?window=&mac=&interface=&group=mac&sort=total&limit=&offset=` | 排序后的流量记录 |
| `GET /api/v1/summary?mac=` | 一次返回某设备（或全部设备）所有窗口的合计，附实时速率 |
| `GET /api/v1/series?mac=&days=30` | 按天的序列，从旧到新，无流量的日期补零 |
| `GET /api/v1/live?mac=` | 最近一个采样周期内每台设备的速率（字节/秒） |

`ingress_*` / `egress_*` 是同一组数字的接口视角写法（ingress = 上传，egress = 下载），为兼容老客户端保留。`group=mac` 会把同一设备在多个接口上的记录合并，`interfaces` 列出这些接口。结果按 `sort`（`total`、`upload`、`download`、`mac`）排序，并返回 `total_count` 供分页。`interface` 参数也可写作 `iface`。

## 从 1.x 升级

- 1.x 的配置结构体没有 JSON 标签，配置文件里所有下划线字段都被**静默忽略**，实际监听 `127.0.0.1:8080`、数据库为工作目录下的 `./traffic-count.db`。2.x 会按文件生效，请确认 `bind_address` 与 wlt 的 `traffic_api_url` 一致。
- 1.x 的数据本身是错的（上下行/设备混淆、重复计数），不做迁移。2.x 新建 `device_traffic_daily` / `device_traffic_total` 两张表，旧表原样保留。
- 目标机器不再需要 clang、LLVM、libbpf。
