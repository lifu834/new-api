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
