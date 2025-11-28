# Nginx 配置文件说明

本目录包含生产用的 Nginx 配置文件。

## 📁 配置文件清单

### `app.conf` - 生产环境配置 ⭐
**用途**: 生产环境标准配置  
**特性**:
- ✅ HTTPS (Let's Encrypt SSL)
- ✅ HTTP → HTTPS 自动跳转
- ✅ 前端静态文件服务
- ✅ API 反向代理 (`/api/*`)
- ✅ 健康检查转发 (`/healthz`)

## 🔄 切换配置文件

```bash
docker compose up -d
```

---

## 🛠️ 常见问题

### Q: 如何验证配置文件语法？
**A**:
```bash
docker compose exec nginx nginx -t
```

### Q: 如何查看当前使用的配置？
**A**:
```bash
docker compose exec nginx cat /etc/nginx/conf.d/app.conf | head -20
```

---

## 🔗 相关文档

- [部署模式说明](../../docs/DEPLOYMENT_MODES.md)
- [本地测试清单](../../docs/LOCAL_TEST_CHECKLIST.md)
