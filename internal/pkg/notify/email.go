package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodshunter/internal/config"
	"goodshunter/internal/model"

	"gopkg.in/gomail.v2"
)

// EmailNotifier 实现邮件通知。
type EmailNotifier struct {
	cfg    *config.EmailConfig
	logger *slog.Logger
}

// NewEmailNotifier 创建一个新的邮件通知器。
func NewEmailNotifier(cfg *config.EmailConfig, logger *slog.Logger) *EmailNotifier {
	return &EmailNotifier{
		cfg:    cfg,
		logger: logger,
	}
}

// Send 发送邮件通知。
func (n *EmailNotifier) Send(ctx context.Context, item *model.Item, reason string, keyword string, oldPrice int32, toEmail string) error {
	if n.cfg.SMTPHost == "" || n.cfg.SMTPUser == "" || n.cfg.FromEmail == "" {
		n.logger.Warn("email config missing, skip notification")
		return nil
	}
	if strings.TrimSpace(toEmail) == "" {
		n.logger.Warn("email recipient empty, skip notification")
		return nil
	}

	m := gomail.NewMessage()
	m.SetHeader("From", n.cfg.FromEmail)
	m.SetHeader("To", toEmail)
	m.SetHeader("Subject", "[GoodsHunter] 🎯 捡漏提醒")

	body := n.buildHTMLBody(item, reason, keyword, oldPrice)
	m.SetBody("text/html", body)

	d := gomail.NewDialer(n.cfg.SMTPHost, n.cfg.SMTPPort, n.cfg.SMTPUser, n.cfg.SMTPPass)

	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("send email: %w", err)
	}

	n.logger.Info("email notification sent", slog.String("to", toEmail), slog.String("reason", reason))
	return nil
}

// SendVerificationCode 发送邮箱验证码。
func (n *EmailNotifier) SendVerificationCode(toEmail string, code string) error {
	if n.cfg.SMTPHost == "" || n.cfg.SMTPUser == "" || n.cfg.FromEmail == "" {
		return fmt.Errorf("email config missing")
	}
	if strings.TrimSpace(toEmail) == "" {
		return fmt.Errorf("empty recipient")
	}

	m := gomail.NewMessage()
	m.SetHeader("From", n.cfg.FromEmail)
	m.SetHeader("To", toEmail)
	m.SetHeader("Subject", "[GoodsHunter] 邮箱验证码")

	body := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family: Arial, sans-serif;">
  <div style="max-width: 520px; margin: 0 auto; padding: 16px;">
    <h2>GoodsHunter 邮箱验证</h2>
    <p>你的验证码是：</p>
    <div style="font-size: 28px; font-weight: bold; letter-spacing: 3px;">%s</div>
    <p>验证码有效期 10 分钟。</p>
  </div>
</body>
</html>`, code)
	m.SetBody("text/html", body)

	d := gomail.NewDialer(n.cfg.SMTPHost, n.cfg.SMTPPort, n.cfg.SMTPUser, n.cfg.SMTPPass)
	if err := d.DialAndSend(m); err != nil {
		return fmt.Errorf("send email: %w", err)
	}

	n.logger.Info("verification email sent", slog.String("to", toEmail))
	return nil
}

func (n *EmailNotifier) buildHTMLBody(item *model.Item, reason string, keyword string, oldPrice int32) string {
	priceLine := fmt.Sprintf("¥ %s", formatJPY(item.Price))
	if reason == "Price Drop Detected" && oldPrice > 0 {
		priceLine = fmt.Sprintf("¥ %s → ¥ %s 📉", formatJPY(oldPrice), formatJPY(item.Price))
	}

	template := `
<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8" />
<style>
  body { font-family: Arial, sans-serif; background: #f6f7fb; color: #1f2937; }
  .card { max-width: 600px; margin: 24px auto; background: #ffffff; border-radius: 12px; overflow: hidden; border: 1px solid #e5e7eb; }
  .header { background: #0f172a; color: #ffffff; padding: 16px 20px; font-size: 16px; font-weight: bold; }
  .content { padding: 20px; }
  .hero img { width: 100%%; max-width: 520px; display: block; margin: 0 auto 16px; border-radius: 8px; }
  .price { font-size: 26px; font-weight: bold; color: #ef4444; margin: 8px 0 12px; }
  .title { font-size: 16px; margin-bottom: 16px; }
  .cta { display: inline-block; padding: 12px 20px; background: #22c55e; color: #fff; text-decoration: none; border-radius: 8px; font-weight: bold; }
  .footer { margin-top: 20px; font-size: 12px; color: #6b7280; }
</style>
</head>
<body>
  <div class="card">
    <div class="header">[GoodsHunter] 🎯 捡漏提醒</div>
    <div class="content">
      <div class="hero"><img src="%s" alt="Item Image" /></div>
      <div class="price">%s</div>
      <div class="title">%s</div>
      <div style="text-align:center; margin-bottom: 12px;">
        <a class="cta" href="%s" target="_blank">立即去煤炉抢购</a>
      </div>
      <div class="footer">触发关键词: %s</div>
    </div>
  </div>
</body>
</html>`

	return fmt.Sprintf(template, item.ImageURL, priceLine, item.Title, item.ItemURL, keyword)
}

func formatJPY(v int32) string {
	s := fmt.Sprintf("%d", v)
	n := len(s)
	if n <= 3 {
		return s
	}
	out := make([]byte, 0, n+2)
	for i, ch := range []byte(s) {
		out = append(out, ch)
		if (n-i-1)%3 == 0 && i != n-1 {
			out = append(out, ',')
		}
	}
	return string(out)
}
