# ADR-0005: 自适应限流机制

## 状态

已采纳

## 背景

即使有速率限制，目标网站仍可能因为以下原因触发封锁：

1. **IP 信誉**: 共享 IP 或数据中心 IP 被标记
2. **行为检测**: 请求模式被识别为爬虫
3. **临时限制**: 网站临时加强防护

连续被封锁后继续高频请求只会浪费资源并延长封锁时间。

## 决策

实现 **自适应限流 (Adaptive Throttling)**：连续封锁达到阈值后自动进入冷却期。

### 策略参数

| 参数 | 值 | 说明 |
|------|-----|------|
| 封锁阈值 | 3 次 | 连续封锁 N 次后触发冷却 |
| 冷却时长 | 30-60 秒 | 随机休眠时间 |
| 计数器 TTL | 10 分钟 | 超时后自动重置 |
| 成功重置 | 立即 | 一次成功即重置计数器 |

### 工作流程

```
┌─────────────┐
│ 正常模式    │
└──────┬──────┘
       │
       ▼
┌─────────────────────┐
│ 请求返回            │
└──────┬──────────────┘
       │
   ┌───┴───┐
   │成功？ │
   └───┬───┘
       │
   是  │  否 (被封锁)
   ▼   │   ▼
重置    │  连续计数 +1
计数器  │
       │
       │  连续 >= 3?
       │   ▼
       │  是: 进入冷却 (30-60s)
       │  否: 继续
       ▼
┌─────────────┐
│ 继续处理    │
└─────────────┘
```

### Redis 同步

多个 Worker 共享封锁计数器：

```
Key: goodshunter:crawler:consecutive_blocks
Value: 连续封锁次数
TTL: 10 分钟
```

使用 Redis INCR 原子递增，确保跨节点一致。

## 考虑的选项

### 选项 1: 固定延迟

**优点:**
- 实现简单
- 可预测

**缺点:**
- 不适应实际情况
- 浪费时间或仍被封锁

### 选项 2: 指数退避

**优点:**
- 逐步增加等待时间
- 适应持续封锁

**缺点:**
- 退避时间可能过长
- 恢复后仍需等待

### 选项 3: 基于成功率动态调整

**优点:**
- 精细控制
- 按比例调整

**缺点:**
- 实现复杂
- 需要维护滑动窗口

### 选项 4: 阈值触发冷却 (已采纳)

**优点:**
- 简单直观
- 一次成功即恢复
- 跨节点同步

**缺点:**
- 固定阈值可能不适合所有场景
- 短暂冷却可能不够

## 后果

### 正面

- **快速响应**: 连续封锁时自动降速
- **快速恢复**: 一次成功立即恢复正常
- **跨节点协调**: 所有 Worker 共享状态
- **资源节约**: 避免无效请求

### 负面

- **冷却延迟**: 所有任务都需等待冷却完成
- **误判风险**: 偶发网络问题可能触发冷却

## 封锁类型分类

| 类型 | 检测信号 | 触发冷却 |
|------|----------|----------|
| `cloudflare_challenge` | JS Challenge, Turnstile | 是 |
| `captcha` | reCAPTCHA, hCAPTCHA | 是 |
| `403_forbidden` | 403 状态码 | 是 |
| `429_rate_limited` | 429 状态码 | 是 |
| `blank_page` | 空白页面 | 是 |
| `connection_error` | 网络错误 | 否 |
| `timeout` | 超时 | 否 |

## 实现细节

### 记录封锁

```go
func (at *AdaptiveThrottler) RecordBlock(ctx context.Context, taskID, blockType string) (shouldSleep bool, sleepDuration time.Duration) {
    count := at.consecutiveBlocks.Add(1)
    
    // 同步到 Redis
    pipe := at.rdb.Pipeline()
    pipe.Incr(ctx, consecutiveBlockKey)
    pipe.Expire(ctx, consecutiveBlockKey, 10*time.Minute)
    
    if count >= 3 && !at.inCooldown.Load() {
        at.inCooldown.Store(true)
        sleepDuration = 30s + random(0, 30s)
        return true, sleepDuration
    }
    return false, 0
}
```

### 记录成功

```go
func (at *AdaptiveThrottler) RecordSuccess(ctx context.Context) {
    at.consecutiveBlocks.Swap(0)
    at.inCooldown.Store(false)
    at.rdb.Del(ctx, consecutiveBlockKey)
}
```

## 实现位置

- `internal/crawler/adaptive.go` (AdaptiveThrottler)

## 监控

建议监控指标：
- 连续封锁次数
- 冷却触发次数
- 冷却持续时间
- 按封锁类型分类的计数
