package constant

import "testing"

func TestTaskBillingUnit(t *testing.T) {
	cases := []struct {
		name string
		in   []int
		want string
	}{
		{"dimensio only", []int{ChannelTypeDimensioVideo}, BillingUnitSecond},
		{"sora-shaped video2api", []int{ChannelTypeSora}, BillingUnitSecond},
		{"kling per call", []int{ChannelTypeKling}, BillingUnitCall},
		{"doubao + meaicc per call", []int{ChannelTypeDoubaoVideo, ChannelTypeMeaiccVideo}, BillingUnitCall},
		{"mixed second and call", []int{ChannelTypeSecureSkillVideo, ChannelTypeKling}, BillingUnitMixed},
		{"image chat per call", []int{ChannelTypeChatGPT2ApiImage}, BillingUnitCall},
		{"empty defaults to call", nil, BillingUnitCall},
	}
	for _, c := range cases {
		if got := TaskBillingUnit(c.in); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestTaskBillingUnitForModel_PerCallSKU(t *testing.T) {
	sora := []int{ChannelTypeSora}
	if got := TaskBillingUnitForModel("seedance-2.0", sora); got != BillingUnitCall {
		t.Errorf("seedance-2.0 on sora channel: got %s want call", got)
	}
	if got := TaskBillingUnitForModel("MiniMax-H3-2k", sora); got != BillingUnitCall {
		t.Errorf("minimax case-insensitive: got %s want call", got)
	}
	if got := TaskBillingUnitForModel("sd-2.5-720p", sora); got != BillingUnitSecond {
		t.Errorf("sd-2.5-720p stays per-second: got %s", got)
	}
	if IsPerCallVideoSKU("seedance-2.0-fast-720p") {
		t.Errorf("prefix must not match: only exact SKU names are per-call")
	}
}
