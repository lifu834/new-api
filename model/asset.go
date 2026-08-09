package model

import (
	"time"
)

// Asset 是用户上传的参考素材。
//
// 为什么要自建（260808 逐家实测得出，不是想当然）：
//
//	所有视频上游都只收**公网 http(s) URL**，而客户手里往往只有本地文件。
//	各家对"直接上传"的支持又互相矛盾：
//	  · dimensio 收 multipart 本地文件，mai **静默忽略**（照常出片、照常收费，
//	    成片与素材无关）
//	  · mai 收 base64 Data URI，dimensio 明确拒绝
//	于是同一个请求在主备之间行为不同 —— 海外组的"双上游互备"就是假的。
//	把素材统一转存成我们自己的 URL，是让各上游行为一致的唯一办法。
//
// 参考了 888 与 secure-skill 的做法，但两家的"素材库"收的都是 **URL 而非文件**
// （只解决"链接会过期"，没解决"客户没有公网地址"），所以我们比它们多做一层真存储。
//
// ⚠️ 列名用 asset_key 而不是 key：key 在 MySQL 与 PostgreSQL 都是保留字，
// 项目要三种数据库同时兼容（见 CLAUDE.md 规则 2）。新表直接避开保留字，
// 比到处套 commonKeyCol 更省事也更不容易漏。
type Asset struct {
	Id       int    `json:"id" gorm:"primaryKey"`
	AssetKey string `json:"key" gorm:"type:varchar(64);uniqueIndex"` // 对外可见的不可枚举 ID

	UserId  int `json:"user_id" gorm:"index"`
	TokenId int `json:"token_id" gorm:"index"` // 绑定到具体 API Key（借鉴 secure-skill 的粒度）

	Filename string `json:"filename" gorm:"type:varchar(255)"`
	MimeType string `json:"mime_type" gorm:"type:varchar(100)"`
	Ext      string `json:"ext" gorm:"type:varchar(16)"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256" gorm:"type:varchar(64);index"`

	// IdempotencyKey 让重复提交同一素材不产生重复记录。上传接口是最容易被
	// 客户端自动重试打爆的地方（借鉴 secure-skill）。
	IdempotencyKey string `json:"-" gorm:"type:varchar(128);index"`

	// 审计：素材**匿名可读**（上游要能拉），出问题必须能追溯到人
	UploadIP  string `json:"-" gorm:"type:varchar(64)"`
	UserAgent string `json:"-" gorm:"type:varchar(255)"`

	CreatedAt int64 `json:"created_at"`
	ExpiresAt int64 `json:"expires_at" gorm:"index"`
	DeletedAt int64 `json:"-" gorm:"index"` // 软删除；0 表示未删
}

func (Asset) TableName() string { return "assets" }

// Alive 判断素材是否仍可用（未删且未过期）。
func (a *Asset) Alive() bool {
	return a.DeletedAt == 0 && (a.ExpiresAt == 0 || a.ExpiresAt > time.Now().Unix())
}

func (a *Asset) Insert() error {
	return DB.Create(a).Error
}

// GetAssetByKey 按对外 ID 取素材。取件端点是**匿名**的（上游要能拉），
// 所以这里不带 user 过滤；调用方负责判断 Alive。
func GetAssetByKey(key string) (*Asset, error) {
	var a Asset
	if err := DB.Where("asset_key = ?", key).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// GetAssetByIdempotency 复用同一 (token, Idempotency-Key) 的既有素材。
// 查不到不是错误——上传照常进行。
func GetAssetByIdempotency(tokenId int, idemKey string) *Asset {
	if idemKey == "" {
		return nil
	}
	var a Asset
	err := DB.Where("token_id = ? AND idempotency_key = ? AND deleted_at = 0",
		tokenId, idemKey).First(&a).Error
	if err != nil || !a.Alive() {
		return nil
	}
	return &a
}

// ListAssetsByUser 列出用户存活的素材。
func ListAssetsByUser(userId int, offset, limit int) ([]*Asset, int64, error) {
	var (
		items []*Asset
		total int64
	)
	now := time.Now().Unix()
	q := DB.Model(&Asset{}).Where("user_id = ? AND deleted_at = 0 AND expires_at > ?", userId, now)
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// UserAssetUsage 返回用户当前占用的字节数与对象数，用于配额判断。
func UserAssetUsage(userId int) (int64, int64, error) {
	var r struct {
		Bytes int64
		Cnt   int64
	}
	now := time.Now().Unix()
	err := DB.Model(&Asset{}).
		Select("COALESCE(SUM(bytes),0) AS bytes, COUNT(*) AS cnt").
		Where("user_id = ? AND deleted_at = 0 AND expires_at > ?", userId, now).
		Scan(&r).Error
	return r.Bytes, r.Cnt, err
}

// SoftDeleteAsset 按对外 ID 软删除，限本人。
func SoftDeleteAsset(userId int, key string) (bool, error) {
	res := DB.Model(&Asset{}).
		Where("asset_key = ? AND user_id = ? AND deleted_at = 0", key, userId).
		Update("deleted_at", time.Now().Unix())
	return res.RowsAffected > 0, res.Error
}

// SweepableAssets 取已过期或已软删、可以清理磁盘的记录。
func SweepableAssets(limit int) ([]*Asset, error) {
	var items []*Asset
	now := time.Now().Unix()
	err := DB.Where("(expires_at > 0 AND expires_at < ?) OR deleted_at > 0", now).
		Order("id ASC").Limit(limit).Find(&items).Error
	return items, err
}

// PurgeAssetRows 物理删除已清理磁盘的记录。
func PurgeAssetRows(ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	return DB.Where("id IN ?", ids).Delete(&Asset{}).Error
}
