package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
)

// TestGetRandomSatisfiedChannel_TypeFilter proves the endpoint-aware channel
// filter is actually applied inside the real memory-cache selection path:
// when a single model (gpt-image-2) is served by BOTH a synchronous channel
// (type 1 / OpenAI) and an async task channel (type 58 / ChatGPT2ApiImage) in
// the SAME group at the SAME priority, the type filter deterministically
// selects the correct channel per endpoint — never the wrong one.
func TestGetRandomSatisfiedChannel_TypeFilter(t *testing.T) {
	// save & restore package globals to avoid cross-test pollution
	savedIDM := channelsIDM
	savedG2M := group2model2channels
	savedMem := common.MemoryCacheEnabled
	defer func() {
		channelsIDM = savedIDM
		group2model2channels = savedG2M
		common.MemoryCacheEnabled = savedMem
	}()

	common.MemoryCacheEnabled = true
	prio := int64(0)
	wt := uint(1)
	sync := &Channel{Id: 9001, Type: 1, Status: 1, Priority: &prio, Weight: &wt}   // OpenAI, dual (sora video task adaptor) but sync-capable
	async := &Channel{Id: 9002, Type: 58, Status: 1, Priority: &prio, Weight: &wt} // ChatGPT2ApiImage, task-only
	channelsIDM = map[int]*Channel{9001: sync, 9002: async}
	group2model2channels = map[string]map[string][]int{
		"routetest": {"gpt-image-2": {9001, 9002}},
	}

	// filters mirror service.buildChannelTypeFilter outputs:
	imageAsyncFilter := func(channelType int) bool { return channelType == 58 } // channelTypeSupportsImageAsync
	syncFilter := func(channelType int) bool { return channelType != 58 }       // !channelTypeIsTaskOnly (type-58 is task-only)

	const N = 50
	for i := 0; i < N; i++ {
		ch, err := GetRandomSatisfiedChannel("routetest", "gpt-image-2", 0, imageAsyncFilter)
		if err != nil || ch == nil {
			t.Fatalf("image-async: unexpected nil/err: ch=%v err=%v", ch, err)
		}
		if ch.Id != 9002 || ch.Type != 58 {
			t.Fatalf("image-async iter %d: expected task channel 9002/type58, got %d/type%d", i, ch.Id, ch.Type)
		}
	}
	for i := 0; i < N; i++ {
		ch, err := GetRandomSatisfiedChannel("routetest", "gpt-image-2", 0, syncFilter)
		if err != nil || ch == nil {
			t.Fatalf("sync: unexpected nil/err: ch=%v err=%v", ch, err)
		}
		if ch.Id != 9001 || ch.Type != 1 {
			t.Fatalf("sync iter %d: expected sync channel 9001/type1, got %d/type%d", i, ch.Id, ch.Type)
		}
	}

	// no filter: both eligible — must never error, may return either
	seen := map[int]bool{}
	for i := 0; i < N; i++ {
		ch, err := GetRandomSatisfiedChannel("routetest", "gpt-image-2", 0)
		if err != nil || ch == nil {
			t.Fatalf("no-filter: unexpected nil/err: ch=%v err=%v", ch, err)
		}
		seen[ch.Id] = true
	}
	if !seen[9001] && !seen[9002] {
		t.Fatalf("no-filter: selected neither channel over %d tries", N)
	}
}
