# ADR-0001: Redis 可靠队列模式

## 状态

已采纳

## 背景

GoodsHunter 需要一个任务队列来分发爬取任务给多个 Worker 节点。核心需求：

1. **零任务丢失**: 即使 Worker 崩溃，任务也不能丢失
2. **分布式支持**: 多个 Worker 可以从同一个队列消费
3. **去重**: 同一个任务不应该被重复调度
4. **简单可靠**: 不引入额外的消息队列组件

## 决策

采用 **BRPOPLPUSH 模式 + Janitor 恢复机制**：

```
1. Worker: BRPOPLPUSH tasks → processing (原子操作)
2. Worker: 处理任务
3. Worker: LPUSH results
4. Worker: LREM processing (确认完成)
5. Janitor: 定期扫描 processing，恢复超时任务
```

### 数据结构

```
goodshunter:queue:tasks            # 待处理任务队列 (List)
goodshunter:queue:tasks:processing # 处理中任务队列 (List)
goodshunter:queue:results          # 结果队列 (List)
goodshunter:queue:tasks:pending    # 任务去重集合 (Set)
goodshunter:queue:tasks:started    # 任务开始时间 (Hash)
```

### 关键实现

**原子推送 (Lua 脚本)**:
```lua
local added = redis.call('SADD', KEYS[1], ARGV[1])
if added == 0 then
    return 0  -- 任务已存在
end
redis.call('LPUSH', KEYS[2], ARGV[2])
return 1
```

**Janitor 恢复**:
- 定期扫描 `processing` 队列
- 根据 `started` Hash 中的时间戳判断超时
- 超时任务原子性地移回 `tasks` 队列

## 考虑的选项

### 选项 1: 简单 LPUSH/BRPOP

**优点:**
- 实现简单
- 无额外状态

**缺点:**
- Worker 崩溃时任务丢失
- 无法追踪处理中的任务

### 选项 2: Redis Streams

**优点:**
- 内置消费组支持
- 消息确认机制
- 更好的可观测性

**缺点:**
- API 更复杂
- 需要 Redis 5.0+
- 消息积压需要额外处理

### 选项 3: 外部消息队列 (RabbitMQ/Kafka)

**优点:**
- 成熟的消息语义
- 丰富的特性

**缺点:**
- 引入额外组件和运维成本
- 增加系统复杂度
- 对于当前规模过于重量级

### 选项 4: BRPOPLPUSH + Janitor (已采纳)

**优点:**
- 使用 Redis 原生命令，无额外依赖
- 原子操作保证任务不丢失
- Janitor 提供故障恢复
- 实现相对简单

**缺点:**
- 需要维护 Janitor 定时任务
- 超时判断需要额外的时间戳记录

## 后果

### 正面

- **零丢失保证**: BRPOPLPUSH 原子性确保任务始终在某个队列中
- **自动恢复**: Janitor 自动恢复卡住/崩溃的任务
- **无重复调度**: 使用 Set 进行任务去重
- **简单部署**: 无需额外的消息队列组件

### 负面

- **Janitor 延迟**: 任务恢复有 10-30 分钟延迟
- **内存占用**: processing 队列会暂存任务数据
- **单点依赖**: 依赖 Redis 可用性

## 实现位置

- `internal/pkg/redisqueue/client.go`

## 参考

- [Redis BRPOPLPUSH](https://redis.io/commands/brpoplpush/)
- [Reliable Queue Pattern](https://redis.io/commands/rpoplpush/#pattern-reliable-queue)
