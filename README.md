# Shortlink

一个用 Go 实现的短链接服务：把长链接转换成短码，访问短码时 302 跳转回原地址。

## 技术栈

Go 1.25 · Gin · GORM · MySQL 8 · Redis 7 · Docker

## 功能

- 创建短链，支持可选的过期时间
- 访问短码时返回 302，跳转到原链接
- 短链过期返回 410，不存在返回 404
- Redis Cache-Aside 缓存
- 空值缓存防重复穿透
- singleflight 防热点击穿
- TTL 随机抖动防雪崩
- 布隆过滤器防随机穿透
- 访问统计异步写库（channel + 固定 worker + 批量幂等写）不拖慢跳转
- 统一响应体、JSON 结构化日志、request_id
- 健康检查，以及收到退出信号时优雅关闭
- Docker Compose 一键启动

## 架构

```
                         ┌──────────────┐
  POST /api/v1/links ───▶│              │
                         │   handler    │
  GET /{code} ──────────▶│              │
                         └──────┬───────┘
                                │
                         ┌──────▼───────┐
                         │   service    │
                         └──┬────┬──────┘
                            │    │
              ┌─────────────┘    └──────────────┐
              ▼                                 ▼
        ┌──────────┐                     ┌─────────────┐
        │  Redis   │                     │ singleflight│
        │  缓存/发号│                     │ 合并并发请求 │
        └────┬─────┘                     └──────┬──────┘
             │                                  │
             ▼                                  ▼
        ┌──────────┐                     ┌─────────────┐
        │ Bloom    │──── 一定不存在 ────▶│    404      │
        │ 过滤器    │                     └─────────────┘
        └────┬─────┘
             │ 可能存在
             ▼
        ┌──────────┐
        │  MySQL   │◀──── 异步统计 ──── channel → worker
        └──────────┘
```

## 目录结构

```
.
├── main.go
├── internal/
│   ├── config/       配置读取
│   ├── dao/          MySQL 访问
│   ├── handler/      HTTP 入口
│   ├── middleware/   请求日志 / request_id
│   ├── model/        数据模型
│   ├── service/      业务逻辑
│   └── stats/        异步统计收集器
├── pkg/
│   ├── base62/       短码编码
│   └── bloom/        Redis 位图布隆过滤器
├── scripts/          压测与冒烟脚本
├── Dockerfile
├── docker-compose.yml
└── schema.sql
```

## 两条主要的链路

创建短链：

```
POST /api/v1/links
  -> handler 解析参数、做格式校验
  -> service 校验链接、取号、转成 62 进制
  -> dao 写入 MySQL
  -> 返回短码
```

访问短码：

```
GET /{code}
  -> 先查 Redis
     命中 -> 直接 302
     没命中 -> 问布隆过滤器
                一定不存在 -> 404
                可能存在 -> singleflight 合并并发请求 -> 查 MySQL
                            -> 校验状态和过期时间
                            -> 回填 Redis
                            -> 302
```

## 几个关键设计

### 短码是怎么来的

用 Redis 的 `INCR` 取一个全局自增号，再用 base62 转成字符串，6 位 base62 大约能放 568 亿条。

没有用 MySQL 自增，是因为 `code` 这一列非空且唯一，插入的时候就要有值；但 code 要从 ID 算出来，ID 又要插入成功之后才有。用 MySQL 自增就得先插一行拿 ID、再更新一次，变成两次写库。Redis 是先取号、算 code、一次性插入，简单一些。



### 为什么用 302

301 是永久重定向，浏览器会缓存。用户第二次点的时候，请求根本不会到我的服务器，访问统计就丢了，短链也没法撤销或者改目标。302 每次都会回源，部分压力可以用 Redis 扛。

### 缓存怎么用

用的是 Cache-Aside：读的时候先查缓存，没命中再查数据库并回填；写的时候先写库，再删缓存。

缓存 key 是 `link:{code}`。TTL 取默认 24 小时和短链剩余过期时间里较小的那个，缓存不会比短链活得更久。

### 穿透、击穿、雪崩

- 穿透：查一个根本不存在的短码。使用了空值缓存，查库确认不存在后写一个 60 秒的空值。
- 击穿：一个热点短码刚过期，很多请求同时回源。使用了 singleflight，把同一个 code 的并发请求合并，只让一个去查库。
- 雪崩：很多 key 同时过期。给 TTL 加了 0 到 5 分钟的随机抖动。

### 布隆过滤器

空值缓存只能挡住重复访问同一个不存在的短码。如果攻击者每次换一个新随机码，空值缓存就没用了，每个请求还是会查一次库。

所以我用 Redis 位图实现了一个布隆过滤器：2 MB 位图，7 个哈希函数。缓存没命中时先问它：

- 一定不存在，直接 404；
- 可能存在，再走正常的查库流程。

启动时会从 MySQL 读出全部短码重建一次，创建成功后把新码加进去。删除不用处理，因为布隆过滤器只能加不能删，最坏只是多查一次库。

实测误判率大约 0.23%。

### 访问统计为什么异步

跳转是核心链路，统计不是。如果每次跳转都同步写一次统计库，跳转会被数据库往返拖慢。

项目的做法是：跳转成功后把事件丢进一个带缓冲的 channel，后台只有一个固定 worker，每 2 秒或者每 100 条批量写一次。channel 满了就丢弃，并计数、定时打日志。

写库用的是 `ON DUPLICATE KEY UPDATE`，按短码和日期做幂等累加，重试也不会写重。

### 分层

handler 只处理 HTTP：解析参数、调用 service、写响应。

service 放业务规则：校验链接、取号、判断过期、缓存读写。

dao 只和 MySQL、Redis 打交道。

依赖是单向的：handler -> service -> dao。main 负责把这些东西组装起来。

## 快速开始

### Docker Compose

第一次运行先把 `.env.example` 复制成 `.env`，并修改里面的数据库密码：

```bash
cp .env.example .env
```

Windows PowerShell 可以用 `Copy-Item .env.example .env`。

```bash
docker compose up -d --build
curl http://localhost:8080/health
```

首次启动会：

1. 启动 MySQL（宿主机 3307，避免和本机 3306 冲突），执行 `schema.sql` 建表；
2. 启动 Redis（宿主机 6380，避免和本机 6379 冲突）
3. 构建并启动 Go 服务（8080）；
4. 服务启动时从 MySQL 全量重建布隆过滤器，并校准 Redis 发号器。

### 本地开发

需要本机 MySQL（3306）和 Redis（6379）：

```bash
go run .
```

通过环境变量覆盖连接串：

```bash
MYSQL_DSN="root:<password>@tcp(127.0.0.1:3306)/shortlink?charset=utf8mb4&parseTime=True&loc=Local"
REDIS_ADDR="127.0.0.1:6379"
```

## API

### 创建短链

```http
POST /api/v1/links
Content-Type: application/json

{
  "origin": "https://example.com/very/long/path",
  "expire_at": "2026-12-01T00:00:00+08:00"
}
```

`expire_at` 可选，RFC3339 格式。

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "code": "1A",
    "short_url": "http://localhost:8080/1A"
  }
}
```

### 跳转

```http
GET /{code}
```

- `302`：跳转到原始链接
- `404`：短链不存在
- `410`：短链已过期

### 健康检查

```http
GET /health
```

## 缓存与防护

| 问题 | 方案 |
|---|---|
| 缓存穿透（重复码） | 空值缓存 60 秒 |
| 缓存穿透（随机码） | 布隆过滤器 |
| 缓存击穿 | singleflight 合并同 key 并发 |
| 缓存雪崩 | TTL 加 0~5 分钟随机抖动 |

布隆过滤器参数：`m = 2^24` 位（2 MB），`k = 7`，约支持 160 万短码。

实测误判率（`BLOOM_FPR=1 go test ./pkg/bloom -run TestFalsePositiveRate -v`）：

- 缩比位图 `m = 2^20` 位（128 KB），`m/n ≈ 10`，`k = 7`
- 写入 10 万个短码，查询 1 万个不存在的随机码
- 误判 23 次，约 **0.23%**

## 压测

环境：本机 Windows，服务与压测工具同机运行，100 并发，持续 30 秒，
请求为 `GET /4`（302 跳转）。

| 场景 | QPS | P50 | P99 | 错误率 |
|---|---|---|---|---|
| 缓存命中 | 26,036 | 3.4 ms | 12.7 ms | 0 |
| 关闭缓存（singleflight + MySQL） | 17,473 | 4.7 ms | 33.1 ms | 0 |

结论：缓存使 QPS 提升约 49%，P99 降低约 62%。

说明：同一台机器上客户端与服务端争抢 CPU，数据仅用于相对比较，
不代表线上容量。

## 测试

```bash
# 单元测试
go test ./...

# 布隆过滤器误判率实测（需要 Redis）
BLOOM_FPR=1 go test ./pkg/bloom -run TestFalsePositiveRate -v

# 集成冒烟（需要服务已启动）
.\scripts\smoke.ps1

# 压测并追加到 CSV
.\scripts\bench.ps1 -Scenario cache -Concurrency 100 -Duration 30s
```

## 优雅关闭

服务监听 `SIGINT` / `SIGTERM`：

1. 停止接收新请求，等待正在处理的请求完成（最多 10 秒）；
2. 通知统计 worker 退出，把缓冲区里的数据刷盘；
3. 关闭数据库连接。

## 踩过的坑

**唯一索引冲突。** Redis 计数器被重置之后，`INCR` 从 1 开始，撞上了数据库里已有的短码。后来加了识别 1062、换号重试，以及启动时按已有最大短码校准计数器。

**布隆过滤器重建失败。** 一开始在 pipeline 里用 Lua 脚本重建位图，Redis 重启后脚本缓存被清空，pipeline 不会自动回退到 `EVAL`，直接报 `NOSCRIPT`。后来改成批量 `SETBIT`。

**压测数据对不上。** 有一次 QPS 和请求总数互相矛盾，最后发现单请求大小从 44 字节变成了 69 字节，压的根本不是同一个短码。之后我每次压测都会先做数据自洽校验。

## 当前不足

- 单实例的 singleflight 只能合并本机请求，多实例需要分布式锁或者逻辑过期。
- 布隆过滤器只在启动时重建，运行中不重建。
- 还没有用户体系和权限管理。
- 压测是同机测试，不能代表线上容量。
