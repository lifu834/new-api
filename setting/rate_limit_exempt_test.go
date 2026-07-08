package setting

import "testing"

func TestModelRateLimitExemptGroups(t *testing.T) {
	// 初始为空：任何组都不豁免
	if err := UpdateModelRateLimitExemptGroupsByJSONString(`[]`); err != nil {
		t.Fatalf("空数组应合法: %v", err)
	}
	if IsModelRateLimitExemptGroup("vvip") {
		t.Fatal("空白名单不应豁免 vvip")
	}

	// 正常写入 + 判定
	if err := UpdateModelRateLimitExemptGroupsByJSONString(`["vvip", " enterprise "]`); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if !IsModelRateLimitExemptGroup("vvip") {
		t.Fatal("vvip 应被豁免")
	}
	if !IsModelRateLimitExemptGroup("enterprise") {
		t.Fatal("组名应去除首尾空白后生效")
	}
	if IsModelRateLimitExemptGroup("default") {
		t.Fatal("default 不在白名单，不应豁免")
	}
	if IsModelRateLimitExemptGroup("") {
		t.Fatal("空组名不应豁免")
	}

	// 序列化：排序后的 JSON 数组
	if s := ModelRateLimitExemptGroups2JSONString(); s != `["enterprise","vvip"]` {
		t.Fatalf("序列化不符: %s", s)
	}

	// 覆盖式更新：旧值应被清掉
	if err := UpdateModelRateLimitExemptGroupsByJSONString(`["vvip"]`); err != nil {
		t.Fatalf("覆盖更新失败: %v", err)
	}
	if IsModelRateLimitExemptGroup("enterprise") {
		t.Fatal("覆盖更新后 enterprise 应不再豁免")
	}

	// 校验
	if err := CheckModelRateLimitExemptGroups(`["vvip"]`); err != nil {
		t.Fatalf("合法输入被拒: %v", err)
	}
	if err := CheckModelRateLimitExemptGroups(`{"vvip":1}`); err == nil {
		t.Fatal("对象应被拒（须为字符串数组）")
	}
	if err := CheckModelRateLimitExemptGroups(`["  "]`); err == nil {
		t.Fatal("空白组名应被拒")
	}
	if err := CheckModelRateLimitExemptGroups(`not json`); err == nil {
		t.Fatal("非 JSON 应被拒")
	}
}
