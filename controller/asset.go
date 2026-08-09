package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
)

// 素材库配额与保留期。
//
// 保留 7 天：远大于最长任务耗时（实测高峰可到 26 分钟），也够"同一张脸批量
// 产片"的复用窗口。作为对照，suda 自建素材库只给 **2 小时**（实测其返回的
// expiresAt 就是 +2h，且到点确实 404）——短 TTL 是这行的常态，7 天已经宽裕。
const (
	assetRetention   = 7 * 24 * time.Hour
	assetQuotaBytes  = 1 << 30 // 每用户 1 GB
	assetQuotaCount  = 500     // 每用户 500 个对象
	assetListMaxSize = 100
)

type assetResponse struct {
	Id        string `json:"id"`
	URL       string `json:"url"`
	Filename  string `json:"filename,omitempty"`
	MimeType  string `json:"mime_type"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// assetPublicURL 拼出对外可取的素材地址。
//
// 🔑 优先用 VIDEO_PROXY_BASE_URL（即 API 域名），**不能直接用 ServerAddress**：
// 后者常常指向前端官网域名（本站就是 nycatai.com），那上面没有 /v1 路由，
// 上游拿到这种地址会拉不到素材而任务失败。视频代理早就踩过同一个坑，
// 这里复用它的配置来源，避免两处不一致。
func assetPublicURL(c *gin.Context, key string) string {
	base := strings.TrimRight(constant.VideoProxyBaseURL, "/")
	if base == "" {
		base = strings.TrimRight(system_setting.ServerAddress, "/")
	}
	if base == "" && c.Request != nil {
		scheme := "https"
		if c.Request.TLS == nil && !strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			scheme = "http"
		}
		base = scheme + "://" + c.Request.Host
	}
	return fmt.Sprintf("%s/v1/assets/%s/content", base, key)
}

func toAssetResponse(c *gin.Context, a *model.Asset) assetResponse {
	return assetResponse{
		Id:        a.AssetKey,
		URL:       assetPublicURL(c, a.AssetKey),
		Filename:  a.Filename,
		MimeType:  a.MimeType,
		Bytes:     a.Bytes,
		CreatedAt: a.CreatedAt,
		ExpiresAt: a.ExpiresAt,
	}
}

func assetError(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": msg, "type": "invalid_request_error"}})
}

// UploadAsset 接收 multipart 文件，落盘后返回一个**公网可取**的地址。
//
// 为什么这个端点有必要（260808 逐家实测）：所有视频上游都只收公网 URL，
// 而客户手里常常只有本地文件；各家对直接上传的支持又互相矛盾
// （dimensio 收 multipart、mai 静默忽略；mai 收 base64、dimensio 拒绝）。
// 统一转存成我们自己的 URL，是让素材在各上游之间行为一致的唯一办法。
func UploadAsset(c *gin.Context) {
	userId := c.GetInt("id")
	tokenId := c.GetInt(string(constant.ContextKeyTokenId))

	fileHeader, err := c.FormFile("file")
	if err != nil {
		assetError(c, http.StatusBadRequest, "invalid_request",
			"missing multipart field 'file'")
		return
	}

	// Idempotency-Key：重复提交同一素材不产生重复记录（借鉴 secure-skill）。
	// 上传接口最容易被客户端自动重试打爆。
	idemKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if existing := model.GetAssetByIdempotency(tokenId, idemKey); existing != nil {
		c.JSON(http.StatusOK, toAssetResponse(c, existing))
		return
	}

	// 先按最宽的上限读，具体类型的上限在判定类型后再卡
	maxAny := service.MaxBytesByKind[service.AssetKindVideo]
	if fileHeader.Size > maxAny {
		assetError(c, http.StatusRequestEntityTooLarge, "file_too_large",
			fmt.Sprintf("file exceeds the maximum allowed size of %d MB", maxAny>>20))
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		assetError(c, http.StatusBadRequest, "invalid_request", "cannot read uploaded file")
		return
	}
	defer f.Close()

	data, tooBig, err := service.CopyLimited(f, maxAny)
	if err != nil {
		assetError(c, http.StatusBadRequest, "invalid_request", "cannot read uploaded file")
		return
	}
	if tooBig {
		assetError(c, http.StatusRequestEntityTooLarge, "file_too_large",
			fmt.Sprintf("file exceeds the maximum allowed size of %d MB", maxAny>>20))
		return
	}
	if len(data) == 0 {
		assetError(c, http.StatusBadRequest, "invalid_request", "uploaded file is empty")
		return
	}

	// 🔑 只按内容首部字节判定类型，不信扩展名也不信客户端的 Content-Type。
	kind, detected, ext, ok := service.DetectAssetType(data)
	if !ok {
		assetError(c, http.StatusUnsupportedMediaType, "unsupported_media_type",
			fmt.Sprintf("detected content type %q is not allowed; allowed: %s",
				detected, service.AllowedTypeList()))
		return
	}
	if limit := service.MaxBytesByKind[kind]; int64(len(data)) > limit {
		assetError(c, http.StatusRequestEntityTooLarge, "file_too_large",
			fmt.Sprintf("%s files must be smaller than %d MB", kind, limit>>20))
		return
	}

	// 配额
	usedBytes, usedCount, err := model.UserAssetUsage(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if usedBytes+int64(len(data)) > assetQuotaBytes {
		assetError(c, http.StatusInsufficientStorage, "quota_exceeded",
			fmt.Sprintf("asset storage quota exceeded (%d MB); delete unused assets first",
				int64(assetQuotaBytes)>>20))
		return
	}
	if usedCount+1 > assetQuotaCount {
		assetError(c, http.StatusInsufficientStorage, "quota_exceeded",
			fmt.Sprintf("asset count quota exceeded (%d); delete unused assets first", assetQuotaCount))
		return
	}

	key, err := service.NewAssetKey(ext)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	now := time.Now()
	sum, err := service.SaveAsset(key, now.Unix(), data)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	a := &model.Asset{
		AssetKey:       key,
		UserId:         userId,
		TokenId:        tokenId,
		Filename:       filepath_Base(fileHeader.Filename),
		MimeType:       detected,
		Ext:            ext,
		Bytes:          int64(len(data)),
		SHA256:         sum,
		IdempotencyKey: idemKey,
		UploadIP:       c.ClientIP(),
		UserAgent:      truncate(c.GetHeader("User-Agent"), 255),
		CreatedAt:      now.Unix(),
		ExpiresAt:      now.Add(assetRetention).Unix(),
	}
	if err := a.Insert(); err != nil {
		_ = service.RemoveAsset(key, now.Unix())
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAssetResponse(c, a))
}

// ListAssets 列出本人存活的素材。
func ListAssets(c *gin.Context) {
	userId := c.GetInt("id")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if size < 1 || size > assetListMaxSize {
		size = 20
	}

	items, total, err := model.ListAssetsByUser(userId, (page-1)*size, size)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	out := make([]assetResponse, 0, len(items))
	for _, a := range items {
		out = append(out, toAssetResponse(c, a))
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list", "data": out,
		"total": total, "page": page, "page_size": size,
	})
}

// DeleteAsset 软删除本人素材，磁盘文件由清理任务回收。
func DeleteAsset(c *gin.Context) {
	userId := c.GetInt("id")
	key := c.Param("asset_id")
	ok, err := model.SoftDeleteAsset(userId, key)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !ok {
		assetError(c, http.StatusNotFound, "not_found", "asset not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": key, "deleted": true})
}

// AssetContent 是**匿名**取件端点——上游服务器要能直接拉取，没法带我们的鉴权。
//
// ⚠️ 因此"猜不到"是唯一的访问屏障：AssetKey 是 192 bit 密码学随机数。
// 这里不做也不能做用户校验，但一律不列目录、不接受任何形式的枚举。
func AssetContent(c *gin.Context) {
	key := c.Param("asset_id")
	a, err := model.GetAssetByKey(key)
	if err != nil || a == nil {
		assetError(c, http.StatusNotFound, "not_found", "asset not found or expired")
		return
	}
	if !a.Alive() {
		assetError(c, http.StatusNotFound, "not_found", "asset not found or expired")
		return
	}
	f, st, err := service.OpenAsset(a.AssetKey, a.CreatedAt)
	if err != nil {
		assetError(c, http.StatusNotFound, "not_found", "asset not found or expired")
		return
	}
	defer f.Close()

	c.Header("Content-Type", a.MimeType)
	c.Header("Cache-Control", "public, max-age=3600")
	// 素材是给上游程序拉取的，不该被当成网页渲染
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", "inline")
	http.ServeContent(c.Writer, c.Request, a.AssetKey, time.Unix(a.CreatedAt, 0), f)
	_ = st
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// filepath_Base 只取文件名部分，避免把客户端的路径存进库。
func filepath_Base(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return truncate(name, 255)
}
