# ADR-0003: Cookie 持久化策略

## 状态

已采纳

## 背景

Mercari 等目标网站使用多层反爬机制，包括：

1. **Cloudflare 验证**: 首次访问需要通过 JS Challenge
2. **Session 绑定**: Cookie 与浏览器指纹关联
3. **频率检测**: 短时间内大量请求触发验证

观察发现：通过验证后获得的 Cookie 可以在后续请求中复用，显著降低被挑战的概率。

## 决策

实现 **Redis-based Cookie 持久化**，在多个 Worker 间共享成功的 Cookie。

### 策略设计

| 策略 | 值 | 说明 |
|------|-----|------|
| 缓存位置 | Redis | 支持多 Worker 共享 |
| 缓存 TTL | 30 分钟 | 平衡复用率和新鲜度 |
| 最大使用时长 | 2 小时 | 超过后强制刷新 |
| 随机刷新率 | 5% | 保持 Cookie 池新鲜 |
| 失效处理 | 遇 403 立即清除 | 快速响应失效 |

### 工作流程

```
请求前:
1. 5% 概率跳过缓存 (随机刷新)
2. 检查 Cookie 年龄 > 2h → 强制刷新
3. 加载缓存 Cookie → 应用到页面

请求成功后:
4. 提取 Mercari 相关 Cookie
5. 过滤过期 Cookie
6. 保存到 Redis (TTL 30min)

遇到 403/Challenge:
7. 清除 Redis 缓存
8. 重置 Cookie 创建时间
```

### 数据结构

```
Key: goodshunter:cookies:mercari
Value: JSON 序列化的 Cookie 数组
TTL: 30 分钟
```

## 考虑的选项

### 选项 1: 不复用 Cookie (每次新建)

**优点:**
- 实现简单
- 无状态管理

**缺点:**
- 每次请求都需要通过 Challenge
- 请求延迟高 (Challenge 需要 2-5 秒)
- 增加被封锁的风险

### 选项 2: 本地文件持久化

**优点:**
- 无网络开销
- 简单直接

**缺点:**
- 无法跨 Worker 共享
- Docker 重启后丢失
- 无法集中管理

### 选项 3: 每个 Worker 独立缓存

**优点:**
- 无共享状态
- 隔离性好

**缺点:**
- Worker 越多，"冷启动" 越多
- 每个 Worker 都需要独立通过 Challenge
- 浪费资源

### 选项 4: Redis 共享 Cookie (已采纳)

**优点:**
- 一次 Challenge，多 Worker 复用
- 显著减少 Challenge 频率
- 中心化管理，易于监控和清除

**缺点:**
- Redis 依赖
- 需要处理并发更新
- Cookie 与浏览器指纹关联可能导致失效

## 后果

### 正面

- **Challenge 减少 80%+**: 大多数请求复用已有 Cookie
- **延迟降低**: 跳过 Challenge 可节省 2-5 秒
- **跨节点共享**: 新 Worker 立即获得有效 Cookie
- **自动刷新**: 5% 随机刷新保持 Cookie 池健康

### 负面

- **指纹风险**: Cookie 与浏览器指纹关联时可能失效
- **共享失效**: 一个 Cookie 失效可能影响多个 Worker
- **复杂度增加**: 需要处理缓存一致性

## 实现细节

### Cookie 过滤

只保存 Mercari 相关的 Cookie：
- Domain: `.mercari.com` 或 `jp.mercari.com`
- 排除已过期的 Cookie

### 内存缓存

为减少 Redis 访问，增加本地内存缓存层：
- 内存缓存 TTL: 5 分钟
- 减少 Redis 读取压力

### 失效处理

```go
// 遇到 403 时清除缓存
if blockType == "403_forbidden" {
    cookieManager.ClearCookies(ctx)
}
```

## 实现位置

- `internal/crawler/adaptive.go` (CookieManager)

## 监控指标

建议监控：
- Cookie 命中率
- Challenge 通过次数
- Cookie 刷新频率
