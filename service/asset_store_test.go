package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectAssetTypeIgnoresClientClaims 只信 magic bytes。
//
// 素材是**匿名可读**并挂在我们域名下的：靠扩展名或客户端自述的 Content-Type
// 判断类型等于没有校验，攻击者可以把任意内容伪装成图片托管到我们域名。
func TestDetectAssetTypeIgnoresClientClaims(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 64))
	kind, ct, ext, ok := DetectAssetType(png)
	if !ok || kind != AssetKindImage || ct != "image/png" || ext != "png" {
		t.Errorf("PNG => %v %q %q %v", kind, ct, ext, ok)
	}

	jpg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0}, 64)...)
	if kind, _, ext, ok := DetectAssetType(jpg); !ok || kind != AssetKindImage || ext != "jpg" {
		t.Errorf("JPEG => %v %q %v", kind, ext, ok)
	}

	// 以下都必须被拒：即便攻击者起名 .png 或自报 image/png，内容说了算
	for _, bad := range [][]byte{
		[]byte("<html><body>hi</body></html>"),
		[]byte("#!/bin/sh\necho hi\n"),
		[]byte("MZ\x90\x00\x03"),                                 // Windows PE
		[]byte("\x7fELF\x02\x01\x01"),                            // ELF
		[]byte("<?php echo 1; ?>"),                               // 脚本
		[]byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>"), // SVG 可内嵌脚本
	} {
		if _, ct, _, ok := DetectAssetType(bad); ok {
			t.Errorf("内容 %q 被判定为可接受类型 %q —— 白名单被绕过", string(bad[:min(12, len(bad))]), ct)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestNewAssetKeyUnguessable 取件端点必须匿名（上游要能拉），
// 所以"猜不到"是唯一屏障：ID 必须是足够长的随机串且不重复。
func TestNewAssetKeyUnguessable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		k, err := NewAssetKey("jpg")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(k, ".jpg") {
			t.Fatalf("key %q 缺扩展名", k)
		}
		hexPart := strings.TrimSuffix(k, ".jpg")
		if len(hexPart) != 48 { // 24 字节 = 192 bit
			t.Fatalf("key 随机部分长度 %d，不足以抵抗枚举", len(hexPart))
		}
		if seen[k] {
			t.Fatal("生成了重复的 asset key")
		}
		seen[k] = true
	}
}

// TestAssetDiskPathStaysInRoot 路径必须锁在根目录内。
// AssetKey 来自我们自己生成的十六进制串，但取件时是从 URL 参数拿的 —— 一旦
// 有人传 ../../etc/passwd，绝不能让它逃出素材根目录。
func TestAssetDiskPathStaysInRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ASSET_STORAGE_PATH", root)

	for _, evil := range []string{
		"../../../etc/passwd",
		"..\\..\\windows\\system32\\config\\sam",
		"a/../../../../root/.ssh/id_rsa",
		"%2e%2e%2f%2e%2e%2fetc/passwd",
		strings.Repeat("f", 48) + "/../../x", // 前缀合法但仍带穿越
		"",
	} {
		if p := AssetDiskPath(evil, 0); p != "" {
			t.Errorf("非法 key %q 应被拒绝，却得到路径 %q", evil, p)
		}
		if SanitizeAssetKey(evil) != "" {
			t.Errorf("非法 key %q 未被 SanitizeAssetKey 拒绝", evil)
		}
	}

	// 正常 key 必须落在根目录内
	good, err := NewAssetKey("jpg")
	if err != nil {
		t.Fatal(err)
	}
	clean := filepath.Clean(AssetDiskPath(good, 0))
	if !strings.HasPrefix(clean, filepath.Clean(root)+string(os.PathSeparator)) {
		t.Errorf("合法 key 的路径不在根目录内: %q", clean)
	}
}

func TestSaveOpenRemoveAsset(t *testing.T) {
	t.Setenv("ASSET_STORAGE_PATH", t.TempDir())
	data := []byte("\x89PNG\r\n\x1a\nhello")
	key, err := NewAssetKey("png")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := SaveAsset(key, 0, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 64 {
		t.Errorf("sha256 = %q", sum)
	}

	f, st, err := OpenAsset(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != int64(len(data)) {
		t.Errorf("size = %d, want %d", st.Size(), len(data))
	}
	_ = f.Close()

	if err := RemoveAsset(key, 0); err != nil {
		t.Fatal(err)
	}
	// 再删一次不应报错（清理任务可能重复执行）
	if err := RemoveAsset(key, 0); err != nil {
		t.Errorf("重复删除应视为成功: %v", err)
	}
}

func TestCopyLimited(t *testing.T) {
	data, tooBig, err := CopyLimited(bytes.NewReader(bytes.Repeat([]byte("a"), 100)), 200)
	if err != nil || tooBig || len(data) != 100 {
		t.Errorf("正常读取 => %d %v %v", len(data), tooBig, err)
	}
	_, tooBig, err = CopyLimited(bytes.NewReader(bytes.Repeat([]byte("a"), 300)), 200)
	if err != nil || !tooBig {
		t.Errorf("超限应被识别 => %v %v", tooBig, err)
	}
}
