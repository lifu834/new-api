package billing_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/require"
)

func loadIgnoreList(t *testing.T, jsonValue string) {
	t.Helper()
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting." + IgnoreGroupRatioField: jsonValue,
	}))
}

func TestIsGroupRatioIgnoredMatching(t *testing.T) {
	t.Cleanup(func() { loadIgnoreList(t, `[]`) })

	loadIgnoreList(t, `["gpt-image-2*","ex-gpt-image-2*","dall-e-3"]`)

	cases := []struct {
		model string
		want  bool
	}{
		// 前缀通配：整族命中，包括以后新增的档位
		{"gpt-image-2", true},
		{"gpt-image-2-1k", true},
		{"gpt-image-2-2k", true},
		{"gpt-image-2-4k", true},
		{"gpt-image-2-pool", true},
		{"gpt-image-2-free", true},
		{"ex-gpt-image-2", true},
		{"ex-gpt-image-2-4k", true},
		// 精确名
		{"dall-e-3", true},
		{"dall-e-3-hd", false},
		// 不该被误伤的
		{"gpt-image", false},
		{"gpt-5.6-sol", false},
		{"ex_gpt-image-2-1k", false}, // 下划线写法未登记
		{"claude-sonnet-4-6", false},
		{"", false},
	}
	for _, tc := range cases {
		require.Equalf(t, tc.want, IsGroupRatioIgnored(tc.model), "model=%q", tc.model)
	}
}

// 裸 "*" 会让全站模型都不计分组倍率，必须在匹配和保存两处都被拦住。
func TestBareWildcardIsInert(t *testing.T) {
	t.Cleanup(func() { loadIgnoreList(t, `[]`) })

	loadIgnoreList(t, `["*"]`)
	require.False(t, IsGroupRatioIgnored("claude-sonnet-4-6"))
	require.False(t, IsGroupRatioIgnored("gpt-image-2"))

	require.Error(t, ValidateIgnoreGroupRatio([]string{"*"}))
}

func TestEmptyListIgnoresNothing(t *testing.T) {
	t.Cleanup(func() { loadIgnoreList(t, `[]`) })

	loadIgnoreList(t, `[]`)
	require.False(t, IsGroupRatioIgnored("gpt-image-2"))

	// 空白项不应被当成"匹配一切"的空前缀
	loadIgnoreList(t, `["","   "]`)
	require.False(t, IsGroupRatioIgnored("gpt-image-2"))
}

func TestValidateIgnoreGroupRatio(t *testing.T) {
	require.NoError(t, ValidateIgnoreGroupRatio(nil))
	require.NoError(t, ValidateIgnoreGroupRatio([]string{"gpt-image-2*", "dall-e-3"}))

	require.Error(t, ValidateIgnoreGroupRatio([]string{""}))
	require.Error(t, ValidateIgnoreGroupRatio([]string{"  "}))
	require.Error(t, ValidateIgnoreGroupRatio([]string{"*"}))
	require.Error(t, ValidateIgnoreGroupRatio([]string{"gpt-*-2"}))    // * 不在结尾
	require.Error(t, ValidateIgnoreGroupRatio([]string{"gpt*image*"})) // 多个 *
}

// 配置热更新后必须立刻反映，且删除条目要真的失效（不能残留旧值）
func TestIgnoreListHotReload(t *testing.T) {
	t.Cleanup(func() { loadIgnoreList(t, `[]`) })

	loadIgnoreList(t, `["gpt-image-2*"]`)
	require.True(t, IsGroupRatioIgnored("gpt-image-2-1k"))

	loadIgnoreList(t, `["dall-e-3"]`)
	require.False(t, IsGroupRatioIgnored("gpt-image-2-1k"))
	require.True(t, IsGroupRatioIgnored("dall-e-3"))

	loadIgnoreList(t, `[]`)
	require.False(t, IsGroupRatioIgnored("dall-e-3"))
}
