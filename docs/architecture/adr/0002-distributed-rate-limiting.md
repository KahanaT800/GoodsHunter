# ADR-0002: 分布式速率限制

## 状态

已采纳

## 背景

GoodsHunter 部署多个 Crawler Worker 节点，需要协调所有节点的请求速率，避免触发目标网站的反爬机制。

关键需求：

1. **全局协调**: 所有 Worker 共享同一速率限制
2. **精确控制**: 支持令牌桶算法，允许短时突发
3. **高性能**: 每次请求前都需要检查，延迟要低
4. **容错**: Redis 不可用时能够降级

## 决策

采用 **Redis Lua 脚本实现的 Token Bucket 算法**。

### 算法原理

```
令牌桶:
- 桶容量 = burst (最大突发)
- 令牌填充速率 = rate (每秒令牌数)
- 每次请求消耗 1 个令牌
- 令牌不足时请求被拒绝
```

### Lua 脚本实现

```lua
local key = KEYS[1]
local rate = tonumber(ARGV[1])    -- 每秒令牌数
local burst = tonumber(ARGV[2])   -- 桶容量
local now = tonumber(ARGV[3])     -- 当前时间 (毫秒)
local requested = tonumber(ARGV[4])

-- 获取当前状态
local data = redis.call("HMGET", key, "tokens", "ts")
local tokens = tonumber(data[1]) or burst
local ts = tonumber(data[2]) or now

-- 计算令牌补充
local delta = math.max(0, now - ts)
local refill = (delta * rate) / 1000.0
tokens = math.min(burst, tokens + refill)

-- 检查是否有足够令牌
if tokens < requested then
    redis.call("HMSET", key, "tokens", tokens, "ts", now)
    return 0  -- 拒绝
end

-- 消耗令牌
tokens = tokens - requested
redis.call("HMSET", key, "tokens", tokens, "ts", now)
return 1  -- 允许
```

### 配置参数

| 参数 | 环境变量 | 默认值 | 说明 |
|------|----------|--------|------|
| rate | `APP_RATE_LIMIT` | 3 | 每秒允许的请求数 |
| burst | `APP_RATE_BURST` | 5 | 最大突发请求数 |

## 考虑的选项

### 选项 1: 本地速率限制 (每个 Worker 独立)

**优点:**
- 实现简单，无网络开销
- 无 Redis 依赖

**缺点:**
- 无法全局协调
- N 个 Worker = N 倍请求速率
- 容易触发反爬

### 选项 2: Redis INCR + EXPIRE (固定窗口)

**优点:**
- 实现简单
- 容易理解

**缺点:**
- 窗口边界问题 (两个窗口交界处可能突发 2x 流量)
- 不支持突发控制

### 选项 3: Redis Sorted Set (滑动窗口)

**优点:**
- 精确的滑动窗口
- 无边界问题

**缺点:**
- 每次请求需要记录时间戳
- 内存占用随窗口大小线性增长
- 清理过期记录有开销

### 选项 4: Token Bucket via Lua (已采纳)

**优点:**
- 支持突发流量 (burst)
- 平滑的速率控制
- 单次 Redis 调用 (原子操作)
- 固定内存占用 (只存储 tokens 和 timestamp)

**缺点:**
- Lua 脚本稍复杂
- 依赖 Redis

## 后果

### 正面

- **全局一致**: 所有 Worker 共享同一令牌桶
- **突发支持**: burst 参数允许短时高峰
- **低延迟**: 单次 Redis 调用，通常 < 1ms
- **原子性**: Lua 脚本保证并发安全

### 负面

- **Redis 依赖**: Redis 不可用时需要降级处理
- **时钟依赖**: 依赖客户端时间戳，需保证各节点时钟同步

## 使用示例

```go
limiter := ratelimit.NewRedisRateLimiter(rdb)

// 每次请求前检查
allowed, err := limiter.Allow(ctx, "goodshunter:ratelimit:global", 3, 5)
if !allowed {
    // 等待或跳过
}
```

## 实现位置

- `internal/pkg/ratelimit/ratelimit.go`

## 参考

- [Token Bucket Algorithm](https://en.wikipedia.org/wiki/Token_bucket)
- [Redis Rate Limiting](https://redis.io/glossary/rate-limiting/)
