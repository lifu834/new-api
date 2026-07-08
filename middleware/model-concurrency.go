package middleware

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

var inMemoryConcurrencyLimiter common.InMemoryConcurrencyLimiter

// ModelConcurrencyLimit 按用户身份档位限制「同时 in-flight」的模型请求数。
// 必须挂在 TokenAuth 之后（依赖 userId / UserGroup）。
// 单节点内存计数；多节点需改 Redis 分布式并发（INCR/DECR + 过期兜底）。
func ModelConcurrencyLimit() func(c *gin.Context) {
	inMemoryConcurrencyLimiter.Init()
	return func(c *gin.Context) {
		if !setting.ModelConcurrencyLimitEnabled {
			c.Next()
			return
		}
		userId := c.GetInt("id")
		if userId == 0 {
			c.Next()
			return
		}
		// nycatai: 管理员豁免
		if model.IsAdmin(userId) {
			c.Next()
			return
		}
		// 容量按用户身份档位（与 RPM 同维：UserGroup 优先）
		group := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		if group == "" {
			group = common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
		}
		// nycatai: 豁免分组白名单（如 vvip）整组跳过并发限制
		if setting.IsModelRateLimitExemptGroup(group) {
			c.Next()
			return
		}
		maxConcurrency := setting.ModelConcurrencyLimitCount
		if g, ok := setting.GetGroupConcurrencyLimit(group); ok {
			maxConcurrency = g
		}
		key := "conc:user:" + strconv.Itoa(userId)
		if !inMemoryConcurrencyLimiter.TryAcquire(key, maxConcurrency) {
			abortWithOpenAiMessage(c, http.StatusTooManyRequests,
				fmt.Sprintf("您的并发请求数已达上限（%d），请稍后重试", maxConcurrency))
			return
		}
		// 正常 / 流式 / panic 路径返回时都会执行，确保槽位释放
		defer inMemoryConcurrencyLimiter.Release(key)
		c.Next()
	}
}
