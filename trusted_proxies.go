package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
)

// defaultTrustedProxies 默认信任的前置代理网段：环回 + RFC1918 私网 + IPv6 ULA。
// 宿主机 nginx、docker bridge、k8s 集群网都落在这些网段内，公网客户端不在，
// 所以这个默认值对绝大多数部署无感，同时挡住来自互联网的 XFF 伪造。
var defaultTrustedProxies = []string{
	"127.0.0.0/8",
	"::1/128",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
}

// trustAllProxies 即 gin 的出厂设置（信任所有代理）。
var trustAllProxies = []string{"0.0.0.0/0", "::/0"}

// setupTrustedProxies 配置 gin 的可信代理列表。
//
// gin 默认信任所有代理，此时 c.ClientIP() 会取 X-Forwarded-For 的**最左值** —— 也就是
// 完全由客户端说了算：任何人加一个 `X-Forwarded-For: 9.9.9.9` 就能伪造来源 IP。后果是
// 令牌级 IP 白名单（token.GetIpLimits）形同虚设，注册 / 充值 / 上传留下的 IP 也全不可信。
// 只信任真实前置代理之后，非可信来源发来的 XFF 一律丢弃，ClientIP() 回落到 TCP 对端地址。
//
// 用环境变量 TRUSTED_PROXIES 覆盖（逗号分隔的 IP 或 CIDR，混写均可）：
//   - 不设置：用上面的默认私网列表
//   - `*`：信任所有代理，即 gin 原始行为（只有在确认前置链路会覆写 XFF 时才用）
//   - `none`：谁都不信任，ClientIP() 恒等于 TCP 对端地址
//
// 例：TRUSTED_PROXIES=127.0.0.1,172.18.0.0/16
//
// 注意：这只是应用层兜底。前置 nginx 仍应把 XFF **覆写**成自己测到的对端地址
// （`$remote_addr` / 边缘覆写的 `$http_x_real_ip`），而不是用 `$proxy_add_x_forwarded_for`
// 追加 —— 后者会把客户端自带的 XFF 保留在最左侧。两道防线互为备份。
func setupTrustedProxies(server *gin.Engine) {
	raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))

	var proxies []string
	switch strings.ToLower(raw) {
	case "":
		proxies = defaultTrustedProxies
	case "*", "all":
		proxies = trustAllProxies
	case "none":
		proxies = nil
	default:
		for _, item := range strings.Split(raw, ",") {
			if item = strings.TrimSpace(item); item != "" {
				proxies = append(proxies, item)
			}
		}
	}

	if err := server.SetTrustedProxies(proxies); err != nil {
		common.SysError(fmt.Sprintf("invalid TRUSTED_PROXIES %q: %s, falling back to defaults", raw, err.Error()))
		proxies = defaultTrustedProxies
		if err = server.SetTrustedProxies(proxies); err != nil {
			common.FatalLog("failed to apply default trusted proxies: " + err.Error())
			return
		}
	}

	switch {
	case len(proxies) == 0:
		common.SysLog("trusted proxies: none (client IP = TCP peer address)")
	case len(proxies) == len(trustAllProxies) && proxies[0] == trustAllProxies[0]:
		common.SysLog("trusted proxies: ALL — X-Forwarded-For is client-controlled unless your proxy overwrites it")
	default:
		common.SysLog("trusted proxies: " + strings.Join(proxies, ", "))
	}
}
