package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goodshunter/internal/model"
	"goodshunter/internal/pkg/notify"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Handler 提供注册与登录接口。
type Handler struct {
	db         *gorm.DB
	jwtSecret  []byte
	mailer     *notify.EmailNotifier
	inviteCode string
	logger     *slog.Logger
}

// NewHandler 创建 Auth Handler。
func NewHandler(db *gorm.DB, jwtSecret string, inviteCode string, mailer *notify.EmailNotifier, logger *slog.Logger) *Handler {
	return &Handler{
		db:         db,
		jwtSecret:  []byte(jwtSecret),
		mailer:     mailer,
		inviteCode: strings.TrimSpace(inviteCode),
		logger:     logger,
	}
}

type registerRequest struct {
	Email      string `json:"email" binding:"required,email"`
	Password   string `json:"password" binding:"required,min=6"`
	InviteCode string `json:"invite_code"`
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

type tokenResponse struct {
	Token string `json:"token"`
}

type customClaims struct {
	jwt.RegisteredClaims
	Role string `json:"role"`
}

// Register 创建新用户。
func (h *Handler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.inviteCode == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "registration disabled"})
		return
	}
	if strings.TrimSpace(req.InviteCode) != h.inviteCode {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid invite code"})
		return
	}
	email := strings.TrimSpace(strings.ToLower(req.Email))

	var existing model.User
	err := h.db.Where("email = ?", email).First(&existing).Error
	if err == nil {
		if existing.IsVerified {
			c.JSON(http.StatusConflict, gin.H{"error": "email already exists"})
			return
		}
		if err := h.issueCode(&existing); err != nil {
			if h.logger != nil {
				h.logger.Warn("issue verification code failed", slog.String("email", email), slog.String("error", err.Error()))
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if h.logger != nil {
			h.logger.Info("verification code resent", slog.String("email", email))
		}
		c.JSON(http.StatusOK, gin.H{"message": "verification code sent"})
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query user failed"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash password failed"})
		return
	}

	user := model.User{
		Email:    email,
		Password: string(hash),
		Role:     "admin",
	}
	if err := h.db.Create(&user).Error; err != nil {
		if h.logger != nil {
			h.logger.Error("create user failed", slog.String("email", email), slog.String("error", err.Error()))
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create user failed"})
		return
	}
	if err := h.issueCode(&user); err != nil {
		_ = h.db.Delete(&user).Error
		if h.logger != nil {
			h.logger.Warn("issue verification code failed", slog.String("email", email), slog.String("error", err.Error()))
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if h.logger != nil {
		h.logger.Info("user registered", slog.String("email", email))
	}
	c.JSON(http.StatusOK, gin.H{"message": "verification code sent"})
}

// Login 校验用户并返回 JWT。
func (h *Handler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	email := strings.TrimSpace(strings.ToLower(req.Email))

	var user model.User
	if err := h.db.Where("email = ?", email).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	if !user.IsVerified {
		c.JSON(http.StatusForbidden, gin.H{"error": "email not verified"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	token, err := h.issueToken(user.ID, user.Role)
	if err != nil {
		if h.logger != nil {
			h.logger.Error("sign token failed", slog.String("email", email), slog.String("error", err.Error()))
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "sign token failed"})
		return
	}

	if h.logger != nil {
		h.logger.Info("user logged in", slog.String("email", email), slog.String("role", user.Role))
	}
	c.JSON(http.StatusOK, tokenResponse{Token: token})
}

// Logout 处理注销请求（当前为无状态，直接返回成功）。
func (h *Handler) Logout(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}

// GuestLogin 以游客身份登录并签发 JWT。
func (h *Handler) GuestLogin(c *gin.Context) {
	const demoEmail = "demo@goodshunter.com"
	var user model.User
	err := h.db.Where("email = ?", demoEmail).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(randomString(12)), bcrypt.DefaultCost)
		if hashErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash password failed"})
			return
		}
		user = model.User{
			Email:      demoEmail,
			Password:   string(hash),
			Role:       "guest",
			IsVerified: true,
		}
		if err := h.db.Create(&user).Error; err != nil {
			if h.logger != nil {
				h.logger.Error("create guest failed", slog.String("email", demoEmail), slog.String("error", err.Error()))
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "create guest failed"})
			return
		}
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query guest failed"})
		return
	}

	token, err := h.issueToken(user.ID, user.Role)
	if err != nil {
		if h.logger != nil {
			h.logger.Error("sign token failed", slog.String("email", demoEmail), slog.String("error", err.Error()))
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "sign token failed"})
		return
	}
	if h.logger != nil {
		h.logger.Info("guest logged in", slog.String("email", demoEmail))
	}
	c.JSON(http.StatusOK, tokenResponse{Token: token})
}

// VerifyEmail 校验验证码。
func (h *Handler) VerifyEmail(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
		Code  string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	var user model.User
	if err := h.db.Where("email = ?", email).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid code"})
		return
	}
	if user.IsVerified {
		c.JSON(http.StatusOK, gin.H{"message": "already verified"})
		return
	}
	if user.VerifyCode == "" || user.VerifyCode != strings.TrimSpace(req.Code) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid code"})
		return
	}
	if user.VerifyCodeExpiresAt == nil || time.Now().After(*user.VerifyCodeExpiresAt) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "code expired"})
		return
	}

	user.IsVerified = true
	user.VerifyCode = ""
	user.VerifyCodeExpiresAt = nil
	user.VerifyCodeSentAt = nil
	if err := h.db.Save(&user).Error; err != nil {
		if h.logger != nil {
			h.logger.Error("verify failed", slog.String("email", email), slog.String("error", err.Error()))
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "verify failed"})
		return
	}

	if h.logger != nil {
		h.logger.Info("email verified", slog.String("email", email))
	}
	c.JSON(http.StatusOK, gin.H{"message": "verified"})
}

// ResendCode 重新发送验证码（60 秒频控）。
func (h *Handler) ResendCode(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	email := strings.TrimSpace(strings.ToLower(req.Email))
	var user model.User
	if err := h.db.Where("email = ?", email).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "email not found"})
		return
	}
	if user.IsVerified {
		c.JSON(http.StatusBadRequest, gin.H{"error": "already verified"})
		return
	}

	if user.VerifyCodeSentAt != nil && time.Since(*user.VerifyCodeSentAt) < 60*time.Second {
		remain := int(60 - time.Since(*user.VerifyCodeSentAt).Seconds())
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many requests", "retry_after": remain})
		return
	}

	if err := h.issueCode(&user); err != nil {
		if h.logger != nil {
			h.logger.Warn("resend verification failed", slog.String("email", email), slog.String("error", err.Error()))
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if h.logger != nil {
		h.logger.Info("verification code resent", slog.String("email", email))
	}
	c.JSON(http.StatusOK, gin.H{"message": "verification code sent"})
}

func (h *Handler) issueCode(user *model.User) error {
	code, err := generateCode(6)
	if err != nil {
		return fmt.Errorf("generate code failed")
	}
	exp := time.Now().Add(10 * time.Minute)
	now := time.Now()

	user.VerifyCode = code
	user.VerifyCodeExpiresAt = &exp
	user.VerifyCodeSentAt = &now

	if err := h.db.Save(user).Error; err != nil {
		if h.logger != nil {
			h.logger.Error("save verification code failed", slog.String("email", user.Email), slog.String("error", err.Error()))
		}
		return fmt.Errorf("save code failed")
	}
	if h.mailer == nil {
		return fmt.Errorf("email notifier not configured")
	}
	if err := h.mailer.SendVerificationCode(user.Email, code); err != nil {
		if h.logger != nil {
			h.logger.Warn("send verification email failed", slog.String("email", user.Email), slog.String("error", err.Error()))
		}
		return fmt.Errorf("send verification failed")
	}
	return nil
}

func (h *Handler) issueToken(userID uint, role string) (string, error) {
	claims := customClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmtUint(userID),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Role: role,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(h.jwtSecret)
}

func generateCode(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("invalid code length")
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = '0' + (buf[i] % 10)
	}
	return string(buf), nil
}

func fmtUint(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "guest"
	}
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}
