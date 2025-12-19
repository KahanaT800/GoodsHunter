# GoodsHunter（谷子猎人）设计文档

**项目代号**：GoodsHunter  
**一句话描述**：  
> 基于 Go 微服务架构的 ACGN 周边商品实时监控与捡漏系统，通过任务化监控、增量去重与多渠道通知，帮助用户第一时间发现高性价比商品。

---

## 1. 项目背景与目标

### 1.1 项目背景
ACGN 周边商品（俗称“谷子”）在二手与拍卖平台（如 Mercari、Yahoo Auction）中具有以下典型特征：

- 商品上架时间高度碎片化，低价商品生命周期极短；
- 平台多采用 SPA（单页应用）架构，传统静态爬虫难以适配；
- 用户需要频繁手动刷新与检索，信息获取效率低。

### 1.2 项目目标
GoodsHunter 旨在构建一个 **任务驱动型、实时、可扩展** 的商品监控系统，核心目标包括：

- 将用户关注行为抽象为**监控任务（Task）**；
- 对商品数据进行**周期抓取、增量去重与规则判定**；
- 通过**时间轴 + 通知**的形式向用户呈现可执行信息；
- 在实现过程中系统性展示 **Go 后端工程能力与微服务架构设计能力**。

### 1.3 非目标（Out of Scope）
- 不覆盖全部二手平台（优先支持 Mercari，保留扩展能力）；
- 不参与交易与支付流程，仅提供信息聚合与跳转；
- 不实现复杂 AI 预测模型，仅采用确定性规则引擎。

---

## 2. 系统整体架构

### 2.1 架构设计原则
- **控制层与计算层分离**：API 服务不直接运行浏览器实例；
- **任务驱动模型**：所有采集行为均由任务调度触发；
- **无状态爬虫服务**：Crawler 可随时横向扩展或重启；
- **增量处理优先**：仅对新出现或发生变化的商品进行处理。

### 2.2 系统组件概览

| 组件 | 职责 |
|---|---|
| Web Frontend | 用户交互、任务管理、时间轴展示 |
| API Service（guzi-api） | 业务中枢、鉴权、Scheduler、结果处理、通知 |
| Crawler Service（guzi-crawler） | 页面渲染、DOM 解析、去重检查 |
| MySQL | 持久化用户、任务与商品数据 |
| Redis | 去重、缓存、临时状态存储 |
| Notification Channel | Email / Telegram / Discord |

### 2.3 核心数据流
1. 用户通过前端创建或启动监控任务；
2. API 服务记录任务配置并加入调度队列；
3. Scheduler 按任务周期向 Crawler 发起 gRPC 抓取请求；
4. Crawler 渲染页面并解析商品信息；
5. 新商品或变更结果返回 API 服务；
6. API 服务持久化数据、更新时间轴并触发通知。

### 2.4 服务流程设计（关键调用链）
- 任务创建：`POST /tasks` -> 校验参数 -> 生成任务记录（待运行）-> 返回任务 ID。
- 调度触发：Scheduler 周期扫描 `status=running` 任务 -> 根据 `interval_seconds` 生成执行计划 -> 触发 gRPC `FetchItems`。
- 抓取执行：Crawler 接收 `FetchRequest` -> 打开页面/懒加载滚动 -> DOM 解析 -> Redis 去重/价格变化判定 -> 返回增量结果。
- 结果入库：API 收到结果 -> GORM upsert `items` -> 写入 `task_items` 时间轴 -> 更新任务运行时间与错误字段。
- 通知分发：对符合规则的事件进行频控 -> 按渠道构造模板 -> 发送（Discord/Webhook 等）-> 记录发送状态。
- 观测与恢复：全链路记录日志与指标；连续失败达到阈值时将任务标记为 `failed` 并告警。

---

## 3. 非功能与运维要求
- 性能：单任务抓取端到端延迟（调用 gRPC 至返回结果）≤ 5s；调度周期最小粒度 1 分钟。
- 并发：单实例支持并发抓取任务 ≥ 5；API QPS 50 以内稳定；超限时降级/排队。
- 稳定性：抓取失败自动重试 2 次（指数退避），连续失败标记任务异常并告警。
- 观测性：暴露 Prometheus 指标（抓取成功率、延迟、错误码分布），健康检查 `/healthz`。
- 安全：JWT 鉴权，配置/密钥通过环境变量或密文文件注入，敏感日志脱敏。
- 日志：Crawler 在错误时保存 HTML 片段或截图以便定位。
- WSL2 兼容：优先使用 headless Chrome；若需手动安装，使用 `apt-get install chromium-browser fonts-noto-cjk`，必要时通过 `launcher.New().Bin("/usr/bin/chromium-browser").NoSandbox(true)` 指定路径。

---

## 4. 前端设计（Frontend & UX）

### 3.1 设计目标
- **简单**：减少无关信息干扰；
- **高效**：高频操作可一键完成；
- **视觉舒适**：符合二次元用户审美取向。

### 3.2 核心页面设计

#### 3.2.1 控制台（Dashboard）
- 布局：卡片式 / 看板风格；
- 每张任务卡片包含：
  - 任务名称（如“初音未来 2024”）
  - 状态指示灯  
    - 绿色：运行中  
    - 黄色：暂停  
    - 红色：异常
  - Start / Stop 开关
  - 编辑 / 删除按钮

#### 3.2.2 捡漏时间轴（Timeline）
- 布局：时间流（类似 Twitter / 微博）；
- 商品条目包含：
  - 商品图片（大图）
  - 当前价格（高亮显示）
  - 上架时间（相对时间，如“2分钟前”）
  - 商品直达链接
  - 操作：收藏 / 标记已读
- 事件标签：
  - `New`：首次出现
  - `PriceDrop`：价格下降
  - `Restock`：重新上架

#### 3.2.3 新建任务向导（Task Wizard）
- Step 1：输入关键词与排除词；
- Step 2：设定预算区间（JPY）；
- Step 3：选择通知渠道（Email / Telegram / Discord）。

### 3.3 前端技术方案
- SPA 单页模式（Fetch / AJAX）；
- 后端 REST API；
- 原生 HTML + JS 或轻量 Admin 模板。

---

## 5. 后端架构设计

### 5.1 API 服务（guzi-api）

#### 5.1.1 职责
- 提供 RESTful API；
- 用户鉴权（JWT）；
- 任务管理与调度；
- 接收抓取结果并处理业务逻辑；
- 通知分发。

#### 5.1.2 核心模块
- Auth Middleware
- Task Controller
- Scheduler（Ticker / Cron）
- Result Ingest
- Notification Dispatcher

#### 5.1.3 任务与调度策略
- 状态机：`pending -> running -> paused -> failed -> stopped`，连续失败 N 次（默认 3）进入 failed，手动恢复。
- 调度：最小粒度 1 分钟，调度并发上限（如 5）防止压垮 Crawler，超限排队。
- 重试：抓取调用失败重试 2 次（退避），记录 `last_error` 与 `last_success_at`。
- 背压：当 Crawler 返回资源紧张错误时，API 将任务延后 N 秒再调度。

#### 5.1.4 REST 接口约定（最小集）
- `GET /tasks?status=&page=&size=` 分页查询；`POST /tasks` 创建任务；`POST /tasks/{id}/start|pause|stop` 状态切换。
- `GET /items?task_id=&page=&size=` 时间轴查询，支持事件过滤。
- 统一返回 `{code, message, data}`；429/503 用于限流/背压；请求超时 8s。

### 5.2 爬虫微服务（guzi-crawler）

#### 5.2.1 职责
- 监听 gRPC 抓取请求；
- 管理 Headless Chrome 实例；
- 渲染 SPA 页面并解析 DOM；
- 商品数据去重判断。

#### 5.2.2 设计特点
- 无状态设计；
- 浏览器池限制并发（如最多 5 个 Tab）；
- 页面加载超时控制；
- User-Agent 管理与基础反爬策略。
- 反爬与稳定性：预热浏览器实例；失败自动刷新页面；必要时代理池/UA 池；关键操作超时 15s；错误时保存 HTML/截图便于诊断。

### 5.3 目录结构与代码组织（建议）
```
.
├── cmd/
│   ├── api/              # guzi-api 可执行入口（main.go）
│   └── crawler/          # guzi-crawler 可执行入口（main.go）
├── internal/
│   ├── api/              # API 服务内部模块（router, middleware, handlers）
│   ├── crawler/          # Crawler 内部模块（browser pool, parser, dedup）
│   ├── scheduler/        # Scheduler 逻辑
│   ├── store/            # 数据访问层（gorm, redis）
│   ├── notification/     # 通知渠道实现
│   └── config/           # 配置加载
├── pkg/                  # 可复用库（可选）
├── proto/                # .proto 与生成代码
├── scripts/              # 辅助脚本（如 make proto, lint）
├── docker/               # Dockerfile, compose 相关文件
└── web/                  # 前端静态资源（index.html, main.js）
```
原则：`cmd` 只做参数解析和 wiring；业务逻辑在 `internal`；对外复用的 helper 才放 `pkg`；配置与密钥通过环境变量注入。

---

## 6. gRPC 通信设计

### 6.1 通信模式
- 第一阶段采用同步 RPC（FetchItems）；
- 后续可演进为异步回调模式。

### 6.2 核心数据结构（概念）
- **FetchRequest**
  - task_id
  - keyword / exclude_keywords
  - min_price / max_price
  - limit
- **Item**
  - source
  - source_item_id
  - title
  - price
  - url
  - image_url
  - posted_at
- **错误约定**：使用 gRPC status code；超时 8s；客户端重试 2 次，幂等。

### 6.3 示例 proto 片段
```proto
service CrawlerService {
  rpc FetchItems(FetchRequest) returns (FetchResponse) {}
}

message FetchRequest {
  string task_id = 1;
  string keyword = 2;
  repeated string exclude_keywords = 3;
  int32 min_price = 4;
  int32 max_price = 5;
  int32 limit = 6;
}

message Item {
  string source = 1;
  string source_item_id = 2;
  string title = 3;
  int32 price = 4;
  string url = 5;
  string image_url = 6;
  int64 posted_at = 7;
}
```

---

## 7. 数据存储设计

### 7.1 MySQL 表结构（概要）

#### users
- id
- email
- password_hash
- created_at

#### tasks
- id
- user_id
- keyword
- exclude_keywords
- min_price
- max_price
- interval_seconds
- status
- last_run_at
- last_success_at
- last_error

#### items
- id
- source
- source_item_id
- title
- price
- url
- image_url
- posted_at
- 索引与约束：`items(source, source_item_id)` 唯一约束；`tasks(user_id, status)` 组合索引；`task_items(task_id, created_at)` 索引。
- TTL：历史 `task_items` 归档/清理策略（如保留 90 天）。

#### task_items（时间轴）
- id
- task_id
- item_id
- event_type
- seen
- favorited
- created_at

### 7.2 Redis 设计
- 去重集合  
  - `dedup:{task_id}` → Set / BloomFilter
- 上次价格记录  
  - `last_price:{task_id}:{item_key}`
- Timeline 热缓存（可选）  
  - `timeline:{task_id}`
- 失效策略：去重集合按任务活跃度定期清理（如 7 天未运行则清空）。

---

## 8. 规则与去重策略

### 8.1 去重流程
1. 生成 `item_key = source + source_item_id`；
2. 查询 Redis 去重集合；
3. 不存在则写入 Redis 并返回；
4. 存在则根据价格变化判定事件类型。

### 8.2 规则引擎（Rule Engine Lite）
- 价格低于预算；
- 价格低于历史最低；
- 关键词 AND / NOT 匹配；
- 事件判定阈值：价格下降超过 5% 或固定阈值（可配置）。

---

## 9. 通知系统设计

### 9.1 通知触发条件
- 新商品首次命中规则；
- 商品价格下降并满足阈值。

### 9.2 通知渠道
- Email（SMTP）
- Telegram Bot
- Discord Webhook（可扩展）
- 频控：同一任务同一商品的通知最小间隔 30 分钟；批量合并多条降价事件。
- 模板：包含标题、价格、事件类型、跳转链接、时间。

---

## 10. 技术栈选型

| 领域 | 技术 |
|---|---|
| 编程语言 | Go 1.21+ |
| Web 框架 | Gin |
| 微服务通信 | gRPC + Protobuf |
| 网页采集 | go-rod |
| 数据库 | MySQL 8.0 |
| 缓存 | Redis |
| 配置管理 | Viper |
| 部署 | Docker / Docker Compose |

---

## 11. 开发里程碑（Milestones）

### MS1：Crawler POC
- 单文件抓取 Mercari；
- 解决 SPA 渲染与懒加载问题。

### MS2：gRPC 微服务拆分
- API 与 Crawler 通信；
- proto 定义与代码生成。

### MS3：持久化与增量监控
- 引入 MySQL 与 Redis；
- 定时调度与去重逻辑。

### MS4：Web 接入与通知
- 前端页面接入；
- 邮件 / TG 推送；
- Docker Compose 一键启动。

---

## 12. 可扩展方向
- 多平台支持（Yahoo Auction、Surugaya）；
- 异步事件流（Kafka / Redis Streams）；
- 规则 DSL；
- 基于标签与行为的推荐系统。

---

## 13. 总结
GoodsHunter 是一个以 **任务与事件驱动** 为核心的商品监控系统，其设计重点在于：

- 清晰的系统分层；
- 增量数据处理；
- 微服务通信与工程化能力。

该项目适合作为 Go 后端 / 微服务方向的综合展示项目。
