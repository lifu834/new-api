package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// GetLoginPowChallenge 签发登录 PoW 挑战（前端在登录被要求 PoW 时调用）。
// 关闭 LoginPowEnabled 时返回 enabled:false，前端据此跳过。
func GetLoginPowChallenge(c *gin.Context) {
	if !common.LoginPowEnabled {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"enabled": false}})
		return
	}
	id, seed, difficulty, expiresAt := service.IssueLoginPowChallenge()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"enabled":    true,
			"id":         id,
			"seed":       seed,
			"difficulty": difficulty,
			"expires_at": expiresAt,
		},
	})
}
