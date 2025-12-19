# GoodsHunter 系统规模画像（基于当前 .env）

本文档对当前配置下的处理能力、瓶颈与容量边界进行说明，便于部署与扩容决策。

## 1. 流量入口与调度节奏

触发频率：
- 轮询模式：每 3 分钟（`APP_SCHEDULE_INTERVAL=3m`）扫描数据库中的待执行任务。
- Redis Streams 模式：独立消费循环每 1 秒读取一次 Stream（ticker=1s），不受 3 分钟调度间隔限制；调度间隔仅用于周期性发布任务。

任务缓冲区：
- Redis Streams 的 Stream：服务端限制 `MAXLEN=100000`，同时受 Redis 内存约束，作为第一级缓冲。
- 内存队列：`APP_QUEUE_CAPACITY=1000`，轮询与 Redis 模式下均为阻塞式入队，队列满时对生产端形成背压，不发生直接丢弃。
- 轮询批量：`APP_QUEUE_BATCH_SIZE=100`，轮询模式按 ID 游标分页拉取，批量大小限制为队列容量的 1/5。

## 2. 并发处理能力

- Worker Pool：`APP_WORKER_POOL_SIZE=50`，Scheduler 最多并行处理 50 个任务。
- 实际抓取并发仍受限于全局限流与浏览器并发上限。

## 3. 实际执行瓶颈

- 全局限流：`APP_RATE_LIMIT=3`，即每秒 3 个令牌。
- 桶容量：`APP_RATE_BURST=5`，允许短时峰值。
- 浏览器并发：`BROWSER_MAX_CONCURRENCY=5`，限制同时打开的页面数量。

结论：并发峰值由 `APP_RATE_BURST` 与 `BROWSER_MAX_CONCURRENCY` 共同决定，且当前两者一致，有利于资源匹配与稳定性。

## 4. 超时与失败处理

- 单次任务超时：2 分钟。超时会终止当前任务并记录失败。
- 限流等待：使用与任务相同的 Context 超时预算，等待时间会挤占执行时间。

建议：当 `goodshunter_ratelimit_wait_duration_seconds` 长期偏高时，可调整为更大的总体超时预算（例如 5 分钟），或为限流器设置独立短超时（例如 30 秒）以尽快重试。

## 5. 容量评估与建议

- Worker Pool（50）与 Rate（3）的关系：多数 Worker 会处于等待令牌状态，这是有意的“宽进严出”策略，用于抵御突发流量。
- 浏览器并发与内存：`BROWSER_MAX_CONCURRENCY=5` 在 t3.small（2GB）上已接近上限，不建议随意上调。

在当前配置下，吞吐量上限约为 3 tasks/s，约等于 180 tasks/min。该规模适用于单机生产部署，并具备一定的突发缓冲能力。
