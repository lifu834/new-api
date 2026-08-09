// Package service 的 asset_store 负责素材文件的落盘与读取。
//
// 一期用**本地磁盘 + 经本站域名分发**，不引入对象存储：
//   - 参考图通常 <1MB，7 天保留下总量很小；
//   - 站点已在 Cloudflare 后面，取件天然有边缘缓存；
//   - 不必等外部账号/密钥就绪，能立刻交付可用能力。
//
// 若将来量级上来，把本文件换成 S3/R2 实现即可，上层接口不用动。
package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// AssetRoot 是素材落盘根目录，可用 ASSET_STORAGE_PATH 覆盖。
func AssetRoot() string {
	if p := strings.TrimSpace(os.Getenv("ASSET_STORAGE_PATH")); p != "" {
		return p
	}
	return "/data/assets"
}

// assetKeyPattern 是合法的对外 ID：48 位十六进制 + 可选扩展名。
// 就是 NewAssetKey 生成的形状。
var assetKeyPattern = regexp.MustCompile(`^[0-9a-f]{48}(\.[a-z0-9]{1,8})?$`)

// SanitizeAssetKey 校验并归一化素材 ID，非法一律返回空串。
//
// 🔑 取件端点的 key 来自 **URL 参数**。虽然调用方会先查库、再用库里的值拼路径，
// 但存储层必须自己扛住 ../../etc/passwd 这类输入——不能依赖上层永远记得校验。
// 用白名单正则而不是"过滤掉 .."：后者容易被 %2e%2e、双重编码之类绕过。
func SanitizeAssetKey(key string) string {
	key = strings.TrimSpace(key)
	if !assetKeyPattern.MatchString(key) {
		return ""
	}
	return key
}

// assetSubPath 按日期分目录，避免单目录塞入过多文件。
func assetSubPath(key string, createdAt time.Time) string {
	return filepath.Join(createdAt.UTC().Format("20060102"), key)
}

// AssetDiskPath 返回某素材的绝对路径；key 非法时返回空串（调用方须判空）。
func AssetDiskPath(key string, createdAt int64) string {
	safe := SanitizeAssetKey(key)
	if safe == "" {
		return ""
	}
	return filepath.Join(AssetRoot(), assetSubPath(safe, time.Unix(createdAt, 0)))
}

// NewAssetKey 生成不可枚举的对外 ID。
//
// 🔑 取件端点必须**匿名可访问**（上游服务器要能拉取，没法带我们的鉴权），
// 所以"猜不到"是唯一的访问屏障——必须用密码学随机数，不能用自增 ID 或时间戳。
func NewAssetKey(ext string) (string, error) {
	b := make([]byte, 24) // 192 bit
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := hex.EncodeToString(b)
	if ext != "" {
		key += "." + ext
	}
	return key, nil
}

// SaveAsset 把内容写入磁盘并返回 sha256。
func SaveAsset(key string, createdAt int64, data []byte) (string, error) {
	full := AssetDiskPath(key, createdAt)
	if full == "" {
		return "", fmt.Errorf("invalid asset key")
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("create asset dir failed: %w", err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", fmt.Errorf("write asset failed: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// OpenAsset 打开素材文件。
func OpenAsset(key string, createdAt int64) (*os.File, os.FileInfo, error) {
	full := AssetDiskPath(key, createdAt)
	if full == "" {
		return nil, nil, fmt.Errorf("invalid asset key")
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, st, nil
}

// RemoveAsset 删除磁盘文件；文件本就不存在不算错误。
func RemoveAsset(key string, createdAt int64) error {
	full := AssetDiskPath(key, createdAt)
	if full == "" {
		// key 本就非法，磁盘上不可能有对应文件；当作已清理，好让清理任务能
		// 把这条坏记录一并删掉，而不是永远卡在那里重试。
		return nil
	}
	err := os.Remove(full)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// ============================
// 类型判定
// ============================

// AssetKind 是素材大类，决定体积上限。
type AssetKind string

const (
	AssetKindImage AssetKind = "image"
	AssetKindVideo AssetKind = "video"
	AssetKindAudio AssetKind = "audio"
)

// allowedTypes 是白名单：MIME -> (大类, 扩展名)。
//
// 覆盖各上游共同接受的格式（suda 文档口径：图 jpeg/png/webp、视频 mp4/mov、
// 音频 mp3；另补 wav/m4a 与 gif 以放宽一点）。白名单而非黑名单——
// 素材挂在我们域名下，宁可少收几种也不要收进可执行内容。
var allowedTypes = map[string]struct {
	Kind AssetKind
	Ext  string
}{
	"image/jpeg":      {AssetKindImage, "jpg"},
	"image/png":       {AssetKindImage, "png"},
	"image/webp":      {AssetKindImage, "webp"},
	"image/gif":       {AssetKindImage, "gif"},
	"video/mp4":       {AssetKindVideo, "mp4"},
	"video/quicktime": {AssetKindVideo, "mov"},
	"audio/mpeg":      {AssetKindAudio, "mp3"},
	"audio/wav":       {AssetKindAudio, "wav"},
	"audio/x-wav":     {AssetKindAudio, "wav"},
	"audio/mp4":       {AssetKindAudio, "m4a"},
	"audio/aac":       {AssetKindAudio, "aac"},
}

// MaxBytesByKind 是各类素材的体积上限。
var MaxBytesByKind = map[AssetKind]int64{
	AssetKindImage: 10 << 20,  // 10 MB
	AssetKindAudio: 20 << 20,  // 20 MB
	AssetKindVideo: 100 << 20, // 100 MB
}

// DetectAssetType 依据**内容首部字节**判定类型。
//
// 🔑 只信 magic bytes，不信扩展名，也不信客户端给的 Content-Type：
// 素材是匿名可读并挂在我们域名下的，靠客户端自述类型等于没有校验。
func DetectAssetType(data []byte) (AssetKind, string, string, bool) {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	ct := http.DetectContentType(head)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if t, ok := allowedTypes[ct]; ok {
		return t.Kind, ct, t.Ext, true
	}
	return "", ct, "", false
}

// AllowedTypeList 给错误信息用，列出可接受的类型。
func AllowedTypeList() string {
	seen := map[string]bool{}
	var out []string
	for ct := range allowedTypes {
		if !seen[ct] {
			seen[ct] = true
			out = append(out, ct)
		}
	}
	// 固定顺序，便于测试与阅读
	order := []string{"image/jpeg", "image/png", "image/webp", "image/gif",
		"video/mp4", "video/quicktime", "audio/mpeg", "audio/wav", "audio/mp4", "audio/aac"}
	var ordered []string
	for _, o := range order {
		if seen[o] {
			ordered = append(ordered, o)
		}
	}
	return strings.Join(ordered, ", ")
}

// CopyLimited 从 r 读取至多 limit+1 字节，用于在不吃满内存的前提下判断超限。
func CopyLimited(r io.Reader, limit int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return nil, true, nil
	}
	return data, false, nil
}
