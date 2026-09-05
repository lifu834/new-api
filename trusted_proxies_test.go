package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// clientIPFor 起一个真实的 gin engine，模拟「TCP 对端 = remoteAddr、请求头带 XFF」的请求，
// 返回 c.ClientIP() 的结果 —— 直接验证行为，而不是验证我们塞给 gin 的字符串列表。
func clientIPFor(t *testing.T, trustedProxiesEnv, remoteAddr, xff string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	// 空串与未设置在 os.Getenv 下等价，所以空 env 用例直接这样表达即可。
	t.Setenv("TRUSTED_PROXIES", trustedProxiesEnv)

	server := gin.New()
	setupTrustedProxies(server)

	var got string
	server.GET("/probe", func(c *gin.Context) {
		got = c.ClientIP()
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	server.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestSetupTrustedProxies(t *testing.T) {
	cases := []struct {
		name       string
		env        string
		remoteAddr string
		xff        string
		want       string
	}{
		{
			// 生产形态：nginx 在宿主机，经 docker bridge 网关回源，XFF 已被 nginx 覆写。
			name:       "default trusts docker bridge and honors overwritten xff",
			remoteAddr: "172.18.0.1:38474",
			xff:        "203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			// 直连（未过代理）的公网客户端伪造 XFF —— 必须被丢弃。
			name:       "default rejects forged xff from public peer",
			remoteAddr: "9.9.9.9:1234",
			xff:        "1.2.3.4",
			want:       "9.9.9.9",
		},
		{
			// XFF 被追加而不是覆写时（nginx 配置回退到 $proxy_add_x_forwarded_for），
			// 最右侧那一跳才是可信代理测到的真实地址，gin 从右往左找第一个非可信项。
			name:       "appended xff falls back to rightmost untrusted hop",
			remoteAddr: "172.18.0.1:38474",
			xff:        "9.9.9.9, 203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			name:       "explicit list trusts only listed proxies",
			env:        "127.0.0.1,172.18.0.0/16",
			remoteAddr: "172.18.0.1:38474",
			xff:        "203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			name:       "explicit list rejects unlisted private peer",
			env:        "127.0.0.1,172.18.0.0/16",
			remoteAddr: "10.1.2.3:38474",
			xff:        "203.0.113.9",
			want:       "10.1.2.3",
		},
		{
			name:       "none trusts nobody",
			env:        "none",
			remoteAddr: "127.0.0.1:38474",
			xff:        "203.0.113.9",
			want:       "127.0.0.1",
		},
		{
			name:       "star restores gin legacy trust-all",
			env:        "*",
			remoteAddr: "9.9.9.9:1234",
			xff:        "1.2.3.4",
			want:       "1.2.3.4",
		},
		{
			// 配置写错不能静默失败开，要回落到默认私网列表。
			name:       "invalid value falls back to defaults",
			env:        "not-an-ip",
			remoteAddr: "9.9.9.9:1234",
			xff:        "1.2.3.4",
			want:       "9.9.9.9",
		},
		{
			name:       "no xff header returns peer address",
			remoteAddr: "172.18.0.1:38474",
			want:       "172.18.0.1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientIPFor(t, tc.env, tc.remoteAddr, tc.xff); got != tc.want {
				t.Fatalf("ClientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}
