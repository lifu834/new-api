package controller

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// TaskCallback receives async task-completion PUSHes from upstream chatgpt2api.
//
// Route: POST /api/task-callback/:secret  (no user/token auth — the ONLY auth is
// the shared secret, compared constant-time against TASK_CALLBACK_SECRET).
//
// Auth fails closed: if the server has no secret configured, or the provided
// secret does not match, the request is rejected. On success the pushed snapshot
// is handed to service.HandleTaskCallback, which applies the same CAS-guarded
// terminal transition + billing the poller uses. The response is fast and the
// webhook is acknowledged best-effort (upstream fires once, follows no redirects).
func TaskCallback(c *gin.Context) {
	ctx := c.Request.Context()

	secret := constant.TaskCallbackSecret
	provided := c.Param("secret")
	// Fail closed: reject when unconfigured or mismatched. ConstantTimeCompare
	// returns 0 on length mismatch, so this is safe for arbitrary inputs.
	if secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(provided)) != 1 {
		logger.LogWarn(ctx, fmt.Sprintf("task callback rejected: bad secret client_ip=%s", c.ClientIP()))
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid secret"})
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body failed"})
		return
	}

	var snap service.TaskCallbackSnapshot
	if err := common.Unmarshal(body, &snap); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("task callback: invalid json body=%q", string(body)))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}

	if err := service.HandleTaskCallback(ctx, &snap); err != nil {
		// Best-effort: upstream will not retry, so ack with 200 and just log.
		logger.LogWarn(ctx, fmt.Sprintf("task callback handling error (task=%s status=%s): %s", snap.ID, snap.Status, err.Error()))
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}
