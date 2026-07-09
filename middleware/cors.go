package middleware

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// CORS 自实现（2026-07-09 替换 gin-contrib/cors）：
//  1. 凭据模式下 Access-Control-Allow-Headers 不能用 "*"（规范规定通配符对
//     credentialed 请求按字面量解析）——前端迁 CF Pages 后跨源登录被浏览器以
//     "content-type is not allowed" 拦截的根因。改为回显预检的
//     Access-Control-Request-Headers，语义上等价"允许所有"且合规。
//  2. 非白名单 Origin 不再 403（gin-contrib 行为），改为透传但不发 CORS 头：
//     浏览器侧照样读不到响应；桌面客户端（Electron 等自带 Origin）不再被误杀。
//     CSRF 无忧：session cookie 是 SameSite=Strict，跨站请求根本不带 cookie。
func CORS() gin.HandlerFunc {
	originAllowed := func(origin string) bool {
		allowed := []string{
			"https://nycatai.com",
			"https://keytool.nycatai.com",
			"https://status.nycatai.com",
		}
		for _, a := range allowed {
			if origin == a {
				return true
			}
		}
		if strings.HasSuffix(origin, ".nycatai.com") && strings.HasPrefix(origin, "https://") {
			return true
		}
		// Tailscale 内网管理面板访问
		if strings.HasPrefix(origin, "http://100.") {
			return true
		}
		return false
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" || !originAllowed(origin) {
			c.Next()
			return
		}
		h := c.Writer.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		// CORS() 可能全局+路由组重复挂载，避免 Vary 重复追加
		if !strings.Contains(h.Get("Vary"), "Origin") {
			h.Add("Vary", "Origin")
		}
		if c.Request.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
			reqHeaders := c.GetHeader("Access-Control-Request-Headers")
			if reqHeaders == "" {
				reqHeaders = "Content-Type, Authorization, New-Api-User"
			}
			// 回显而非 "*"：凭据模式下通配符按字面量解析（本次登录白屏事故根因）
			h.Set("Access-Control-Allow-Headers", reqHeaders)
			h.Set("Access-Control-Max-Age", "43200")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func PoweredBy() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-New-Api-Version", common.Version)
		c.Next()
	}
}
