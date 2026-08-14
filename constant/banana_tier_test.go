package constant

import "testing"

func TestBananaTierHelpers(t *testing.T) {
	base := map[string]bool{"nano-banana-pro": true, "nano-banana-2": true}
	hidden := map[string]bool{
		"nano-banana-pro-2k": true, "nano-banana-pro-4k": true,
		"nano-banana-2-2k": true, "nano-banana-2-4k": true,
	}
	// 基名本身不隐藏（要对外列出），档位 SKU 不是基名（不能再被翻档）
	for _, m := range []string{"nano-banana-pro", "nano-banana-2",
		"nano-banana-pro-2k", "nano-banana-pro-4k", "nano-banana-2-2k", "nano-banana-2-4k",
		"nano-banana-pro-8k", "gpt-image-2", "nano-banana", ""} {
		if got := IsBananaTieredBaseModel(m); got != base[m] {
			t.Errorf("IsBananaTieredBaseModel(%q) = %v, want %v", m, got, base[m])
		}
		if got := IsBananaHiddenTierSKU(m); got != hidden[m] {
			t.Errorf("IsBananaHiddenTierSKU(%q) = %v, want %v", m, got, hidden[m])
		}
	}
}
