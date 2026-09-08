package operation_setting

import "testing"

func resetTiers() { LongContextTiers = []LongContextTier{} }

func TestLongContextTierMatch(t *testing.T) {
	resetTiers()
	defer resetTiers()
	if err := LongContextTiersFromString(`[{"prefix":"gpt-5.6","threshold":272000,"input_mult":2,"output_mult":1.5}]`); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// 超阈值命中
	if tier := MatchLongContextTier("gpt-5.6-sol", 272001); tier == nil {
		t.Fatal("272001 应命中")
	} else if tier.InputMult != 2 || tier.OutputMult != 1.5 {
		t.Fatalf("倍数错: %+v", tier)
	}
	// 边界:等于阈值不命中(与上游 >272k 口径一致)
	if MatchLongContextTier("gpt-5.6-sol", 272000) != nil {
		t.Fatal("272000 不应命中(边界是严格大于)")
	}
	// 远低于阈值
	if MatchLongContextTier("gpt-5.6-sol", 1000) != nil {
		t.Fatal("1000 不应命中")
	}
	// 前缀不匹配
	if MatchLongContextTier("claude-opus-5", 500000) != nil {
		t.Fatal("非匹配前缀不应命中")
	}
	// 前缀匹配是 HasPrefix,terra 也应命中同一条规则
	if MatchLongContextTier("gpt-5.6-terra", 300000) == nil {
		t.Fatal("gpt-5.6-terra 应命中 gpt-5.6 规则")
	}
}

func TestLongContextTierEmptyIsOff(t *testing.T) {
	resetTiers()
	defer resetTiers()
	if err := LongContextTiersFromString(""); err != nil {
		t.Fatalf("空串应视为关闭而非报错: %v", err)
	}
	if len(LongContextTiers) != 0 {
		t.Fatal("空串应清空规则表")
	}
	if MatchLongContextTier("gpt-5.6-sol", 10_000_000) != nil {
		t.Fatal("功能关闭时任何请求都不该命中")
	}
}

func TestLongContextTierRejectsDiscount(t *testing.T) {
	resetTiers()
	defer resetTiers()
	// 倍数 <1 是配置错误(会变成打折),必须钳制回 1
	if err := LongContextTiersFromString(`[{"prefix":"gpt-5.6","threshold":272000,"input_mult":0.5,"output_mult":2}]`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	tier := MatchLongContextTier("gpt-5.6-sol", 300000)
	if tier == nil {
		t.Fatal("应命中")
	}
	if tier.InputMult != 1 {
		t.Fatalf("input_mult<1 应钳制为 1, got %v", tier.InputMult)
	}
	if tier.OutputMult != 2 {
		t.Fatalf("合法的 output_mult 应保留, got %v", tier.OutputMult)
	}
}

func TestLongContextTierDropsNoops(t *testing.T) {
	resetTiers()
	defer resetTiers()
	// 全 1 = 不加价,等于没配;阈值非正/前缀空 = 非法
	err := LongContextTiersFromString(`[
		{"prefix":"a","threshold":100,"input_mult":1,"output_mult":1},
		{"prefix":"","threshold":100,"input_mult":2,"output_mult":2},
		{"prefix":"b","threshold":0,"input_mult":2,"output_mult":2},
		{"prefix":"c","threshold":100,"input_mult":2,"output_mult":1}
	]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(LongContextTiers) != 1 || LongContextTiers[0].Prefix != "c" {
		t.Fatalf("只应保留 c, got %+v", LongContextTiers)
	}
}

func TestLongContextTierPicksHighestThreshold(t *testing.T) {
	resetTiers()
	defer resetTiers()
	if err := LongContextTiersFromString(`[
		{"prefix":"gpt-5.6","threshold":272000,"input_mult":2,"output_mult":1.5},
		{"prefix":"gpt-5.6","threshold":1000000,"input_mult":3,"output_mult":2}
	]`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 只过低档
	if tier := MatchLongContextTier("gpt-5.6-sol", 300000); tier == nil || tier.InputMult != 2 {
		t.Fatalf("300k 应落低档, got %+v", tier)
	}
	// 两档都过时取高档
	if tier := MatchLongContextTier("gpt-5.6-sol", 2_000_000); tier == nil || tier.InputMult != 3 {
		t.Fatalf("2M 应落高档, got %+v", tier)
	}
}

func TestLongContextTierRoundTrip(t *testing.T) {
	resetTiers()
	defer resetTiers()
	in := `[{"prefix":"gpt-5.6","threshold":272000,"input_mult":2,"output_mult":1.5}]`
	if err := LongContextTiersFromString(in); err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := LongContextTiersToString()
	resetTiers()
	if err := LongContextTiersFromString(out); err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if len(LongContextTiers) != 1 || LongContextTiers[0].Threshold != 272000 {
		t.Fatalf("回读后规则不一致: %+v", LongContextTiers)
	}
}

func TestLongContextTierBadJSON(t *testing.T) {
	resetTiers()
	defer resetTiers()
	if err := LongContextTiersFromString(`{not json`); err == nil {
		t.Fatal("非法 JSON 应报错(让保存操作失败,而不是静默清空规则)")
	}
}

func TestLongContextTierInclusive(t *testing.T) {
	resetTiers()
	defer resetTiers()
	// grok 系上游用「达到即适用」(>=),需显式 inclusive
	if err := LongContextTiersFromString(`[{"prefix":"grok","threshold":200000,"input_mult":2,"output_mult":2,"inclusive":true}]`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if MatchLongContextTier("grok-4.5", 200000) == nil {
		t.Fatal("inclusive 下等于阈值应命中")
	}
	if MatchLongContextTier("grok-4.5", 199999) != nil {
		t.Fatal("低于阈值仍不应命中")
	}
	// 对照:非 inclusive 的 OpenAI 系等于阈值不命中
	resetTiers()
	if err := LongContextTiersFromString(`[{"prefix":"gpt-5.6","threshold":272000,"input_mult":2,"output_mult":1.5}]`); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if MatchLongContextTier("gpt-5.6-sol", 272000) != nil {
		t.Fatal("非 inclusive 下等于阈值不应命中")
	}
}
