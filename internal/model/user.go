package model

import "time"

// User 表示系统用户。
type User struct {
	ID                  uint       `gorm:"primaryKey"`                     // 用户 ID
	Email               string     `gorm:"type:varchar(191);uniqueIndex"`  // 邮箱（唯一）
	Password            string     `gorm:"not null"`                       // bcrypt 哈希
	Role                string     `gorm:"type:varchar(16);default:admin"` // 角色: admin / guest
	IsVerified          bool       `gorm:"default:false"`                  // 邮箱是否已验证
	VerifyCode          string     `gorm:"type:varchar(16)"`               // 邮箱验证码
	VerifyCodeExpiresAt *time.Time // 验证码过期时间
	VerifyCodeSentAt    *time.Time // 验证码发送时间
	CreatedAt           time.Time  // 创建时间

	Tasks []Task `gorm:"foreignKey:UserID"`
}
