package model

import (
	"time"
)

// Task 表示一个商品监控任务。
//
// 它记录了用户设定的搜索条件（关键词、价格区间、平台等）以及任务的运行状态。
// 任务与商品是多对多关系（通过 task_items 表关联）。
type Task struct {
	ID        uint      `gorm:"primaryKey"` // 任务唯一标识
	CreatedAt time.Time // 创建时间
	UpdatedAt time.Time // 更新时间

	UserID   uint   `gorm:"not null"`          // 所属用户 ID
	User     User   `gorm:"foreignKey:UserID"` // 所属用户
	Keyword  string `gorm:"not null"`          // 搜索关键词
	MinPrice int32  // 最低价格限制（0 表示不限）
	MaxPrice int32  // 最高价格限制（0 表示不限）

	Platform  int `gorm:"not null"`  // 目标平台 (对应 Proto 枚举的整型值)
	SortBy    int `gorm:"not null"`  // 排序方式 (对应 Proto SortBy 枚举的整型值)
	SortOrder int `gorm:"default:0"` // 排序方向 (0: Desc, 1: Asc, 对应 Proto SortOrder)

	Status        string     `gorm:"default:running"` // 任务状态: "running" (运行中) / "stopped" (已停止)
	LastRunAt     *time.Time // 上次执行抓取的时间
	NotifyEnabled bool       `gorm:"default:true"` // 是否开启通知（新品/降价）
	BaselineAt    *time.Time // 基准数据建立时间（首次抓取完成）

	Items []Item `gorm:"many2many:task_items"` // 关联的商品列表
}

// Item 表示从电商平台抓取到的商品信息。
//
// SourceID 是商品在源平台的唯一标识（如 Mercari 的 m123456），用于去重。
type Item struct {
	ID        uint      `gorm:"primaryKey"` // 内部 ID
	CreatedAt time.Time // 首次抓取时间
	UpdatedAt time.Time // 更新时间

	SourceID string `gorm:"type:varchar(191);uniqueIndex;not null"` // 平台原始 ID (唯一索引)
	Title    string // 商品标题
	Price    int32  // 商品价格 (单位: 日元)
	ImageURL string // 主图链接
	ItemURL  string // 商品详情页链接

	Tasks []Task `gorm:"many2many:task_items"` // 关联的任务列表
}

// TaskItem 是任务与商品的关联表（多对多中间表）。
//
// 它记录了某个商品是何时被某个任务抓取到的，用于生成时间轴。
type TaskItem struct {
	TaskID uint `gorm:"primaryKey"` // 任务 ID
	ItemID uint `gorm:"primaryKey"` // 商品 ID

	CreatedAt time.Time // 关联创建时间 (即抓取到该商品的时间)
	Rank      int       `gorm:"default:0"` // 抓取时的排序位置（用于保持列表顺序）
}
