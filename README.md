# API 通知系统（Notification Relay Service）

一个接收业务系统提交的外部 HTTP 通知请求，并尽可能可靠地投递到目标地址的内部服务。

## 问题理解

多个业务系统在关键事件发生时需要通知外部供应商（广告系统、CRM、库存系统等），但每个供应商的 API 地址和格式不同。业务系统不关心返回值，只需要通知"送达"。

**本质上这是一个 fire-and-forget + reliable delivery 的问题**：业务系统提交通知后立即返回，由本服务负责可靠投递。

## 整体架构

```
业务系统 A ─┐
业务系统 B ──┤──> [HTTP API] ──> [SQLite] ──> [Worker] ──> 外部供应商 API
业务系统 C ─┘     (接收入库)     (持久化)    (轮询+投递)
```

单进程架构，包含两个核心组件：

1. **HTTP API Server** — 接收通知请求，持久化到 SQLite，立即返回 `202 Accepted`
2. **Worker** — 轮询数据库，取出待投递的通知，执行 HTTP 请求，处理重试

## 系统边界

### 选择解决的问题

- 通知请求的**持久化接收**（先入库再投递，不丢消息）
- **可靠投递**（重试 + 指数退避 + 抖动）
- **失败处理**（超过最大重试次数标记为 failed）
- **投递状态查询**（GET 接口查看单条通知状态）

### 明确不解决的问题

- **请求体模板/转换**：由调用方组装好完整的 URL、Headers、Body，本服务只做透传。理由：模板引擎增加系统复杂度，且不同供应商格式差异大，难以抽象出通用模板，不如让业务系统自己负责。
- **返回值解析和回调**：题目明确说"不需要关心返回值"。
- **认证和鉴权**：作为内部服务，假设运行在可信网络内。生产环境可加 API Key 或 mTLS。
- **流量控制/限速**：MVP 阶段不做对外部供应商的 rate limiting。未来可按 target domain 配置限速。
- **通知去重**：由业务系统通过 notification ID 保证幂等，本服务不做去重。

## 可靠性与失败处理

### 投递语义：至少一次（At-Least-Once）

- 通知请求先持久化到 SQLite（WAL 模式），然后异步投递
- Worker 取出 `pending` 任务后，通过原子 `UPDATE ... WHERE status = 'pending'` 将状态设为 `delivering`（claim），防止多 worker 重复投递
- 投递成功标记 `delivered`，失败则回退为 `pending` 并设置下次重试时间
- 进程崩溃时，卡在 `delivering` 状态的通知会在重启时被 `RecoverStale` 恢复为 `pending`（超时阈值 2 分钟）
- 因此**外部系统可能收到重复通知**，这是 at-least-once 的固有特性，需由接收方做幂等处理

### 重试策略

- **指数退避 + 抖动**：`delay = 5s * 2^(retry-1) + jitter`
- 默认最大重试 5 次（即首次尝试 + 最多 5 次重试 = 共 6 次尝试），退避序列约为：5s → 10s → 20s → 40s → 80s
- 调用方可通过 `max_retries` 参数自定义
- 抖动为 0~30% 的正向随机偏移（即实际延迟在 `[base, base*1.3]` 区间），避免重试风暴

### 外部系统长期不可用

- 超过最大重试次数后标记为 `failed`
- 通过日志输出 `FAILED` 级别告警（生产环境可接入告警系统）
- 可通过 GET API 查询失败的通知，人工介入后重新提交

## 关键工程决策与取舍

### 1. 为什么用 SQLite 而不是 Redis / RabbitMQ / Kafka？

**SQLite 足够且更简单。** 对于 MVP：
- 零外部依赖，单文件数据库，部署简单
- WAL 模式支持并发读写，性能足够应对中低量级场景
- 天然持久化，进程崩溃不丢数据
- 不引入消息队列 = 不引入消息队列的运维复杂度（broker 高可用、消息堆积、死信队列配置等）

**何时演进**：当单机 SQLite 写入成为瓶颈（预计 > 1000 TPS）时，迁移到 PostgreSQL + LISTEN/NOTIFY 或引入 Redis Stream。

### 2. 为什么用轮询而不是事件驱动？

- 轮询间隔 1 秒，对于通知场景延迟完全可接受
- 实现简单，不需要额外的事件总线
- SQLite 没有原生的 LISTEN/NOTIFY，轮询是自然选择

**何时演进**：切换到 PostgreSQL 后可改用 LISTEN/NOTIFY 降低延迟。

### 3. 为什么单 Worker 而不是 Worker Pool？

- MVP 阶段同步逐条投递，实现简单，行为可预测
- 外部 HTTP 调用超时设为 10 秒，单 Worker 最差吞吐为 0.1 req/s（每个请求都超时的极端情况）；正常情况下外部 API 响应在百毫秒级，吞吐可达 10+ req/s

**何时演进**：用 goroutine pool（如 `errgroup` + semaphore）并发投递，轻松扩展到 50-100 并发。Go 的 goroutine 让这个演进成本很低。

### 4. 为什么选 Go？

- 天然适合这类基础设施服务：单二进制部署、优秀的并发原语、强大的标准库 HTTP 支持
- 未来扩展到 Worker Pool 只需 goroutine + channel，无需引入额外框架
- 相比 PHP（RightCapital 的主栈），Go 在长时间运行的 Worker 进程场景更合适

## 未来演进路径

| 阶段 | 触发条件 | 演进方向 |
|------|---------|---------|
| V1 (当前) | — | SQLite + 单 Worker + 轮询 |
| V1.5 | 需要并发投递 | 加 goroutine Worker Pool |
| V2 | 单机 SQLite 瓶颈 | 迁移 PostgreSQL，改 LISTEN/NOTIFY |
| V3 | 多实例部署需求 | 引入 Redis Stream 或 SQS 做任务分发 |
| V4 | 需要 rate limiting | 按 target domain 配置限速策略 |

## 运行方式

```bash
# 构建
go build -o notify-relay .

# 运行（默认 :8080，数据库文件 notifications.db）
./notify-relay

# 自定义
ADDR=:9090 DB_PATH=/tmp/notify.db ./notify-relay
```

## API 使用

### 提交通知

```bash
curl -X POST http://localhost:8080/api/notifications \
  -H "Content-Type: application/json" \
  -d '{
    "target_url": "https://httpbin.org/post",
    "method": "POST",
    "headers": {"Authorization": "Bearer token123"},
    "body": "{\"event\": \"user_registered\", \"user_id\": 42}"
  }'
```

响应：`202 Accepted`
```json
{"id": "550e8400-e29b-41d4-a716-446655440000", "status": "pending"}
```

### 查询状态

```bash
curl http://localhost:8080/api/notifications/550e8400-e29b-41d4-a716-446655440000
```
