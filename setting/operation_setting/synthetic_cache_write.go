package operation_setting

import "strings"

// SyntheticCacheWriteModels 缓存写自计量的模型前缀列表(每行一个,如 gpt-5.6)。
// 背景:GPT-5.6+ 官方 API 对缓存写按 1.25x 计费并申报 cache_write_tokens,
// 但订阅型上游(号池/采购转售)不计量、申报恒 0,而物理写入真实发生(热读可命中)。
// 对匹配前缀的模型,当上游申报为 0 时按官方语义「未命中输入即写入」推定缓存写量,
// 走 CacheCreationRatio(默认 1.25)计费;真实申报(>0)永远优先,不会被覆盖。
// 空列表 = 功能关闭。
var SyntheticCacheWriteModels = []string{}

func SyntheticCacheWriteModelsToString() string {
	return strings.Join(SyntheticCacheWriteModels, "\n")
}

func SyntheticCacheWriteModelsFromString(s string) {
	SyntheticCacheWriteModels = []string{}
	for _, k := range strings.Split(s, "\n") {
		k = strings.TrimSpace(k)
		if k != "" {
			SyntheticCacheWriteModels = append(SyntheticCacheWriteModels, k)
		}
	}
}

func MatchSyntheticCacheWriteModel(model string) bool {
	for _, prefix := range SyntheticCacheWriteModels {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}
