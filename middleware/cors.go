package middleware

import (
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func CORS() gin.HandlerFunc {
	config := cors.DefaultConfig()
	config.AllowCredentials = true
	config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{"*"}
	config.AllowOriginFunc = func(origin string) bool {
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
	return cors.New(config)
}

func PoweredBy() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-New-Api-Version", common.Version)
		c.Next()
	}
}
