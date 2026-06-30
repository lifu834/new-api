package service

import (
	"testing"
	"time"
)

// resetChannelHealth clears tracker state and applies fast test timings.
func resetChannelHealth(t *testing.T, window, cooldown time.Duration, threshold int) {
	t.Helper()
	channelHealthMu.Lock()
	channelHealth = make(map[int]*channelHealthState)
	channelHealthMu.Unlock()

	origWindow, origCooldown, origThreshold := channelAffinityFailWindow, channelAffinityCooldown, channelAffinityFailThreshold
	channelAffinityFailWindow = window
	channelAffinityCooldown = cooldown
	channelAffinityFailThreshold = threshold
	t.Cleanup(func() {
		channelAffinityFailWindow = origWindow
		channelAffinityCooldown = origCooldown
		channelAffinityFailThreshold = origThreshold
		channelHealthMu.Lock()
		channelHealth = make(map[int]*channelHealthState)
		channelHealthMu.Unlock()
	})
}

func TestChannelHealth_TripsAtThreshold(t *testing.T) {
	resetChannelHealth(t, time.Second, time.Second, 5)
	const ch = 6

	for i := 0; i < 4; i++ {
		RecordChannelFailure(ch)
	}
	if IsChannelAffinityUnhealthy(ch) {
		t.Fatalf("channel should still be healthy after 4 failures (threshold 5)")
	}
	RecordChannelFailure(ch) // 5th within window
	if !IsChannelAffinityUnhealthy(ch) {
		t.Fatalf("channel should be unhealthy after reaching threshold")
	}
}

func TestChannelHealth_FailuresOutsideWindowDoNotTrip(t *testing.T) {
	resetChannelHealth(t, 40*time.Millisecond, time.Second, 5)
	const ch = 7

	// Drip failures slower than the window so they age out before accumulating.
	for i := 0; i < 8; i++ {
		RecordChannelFailure(ch)
		time.Sleep(15 * time.Millisecond)
	}
	if IsChannelAffinityUnhealthy(ch) {
		t.Fatalf("spread-out failures should not trip the cooldown")
	}
}

func TestChannelHealth_SuccessClearsAccumulation(t *testing.T) {
	resetChannelHealth(t, time.Second, time.Second, 5)
	const ch = 8

	for i := 0; i < 4; i++ {
		RecordChannelFailure(ch)
	}
	RecordChannelSuccess(ch) // wipe the 4 pre-trip failures
	RecordChannelFailure(ch) // counts as 1, not 5
	if IsChannelAffinityUnhealthy(ch) {
		t.Fatalf("success should have cleared accumulation, preventing a trip")
	}
}

// The key anti-flap guarantee: once a cooldown is active, a sporadic success on a
// half-failing channel must NOT cancel it. The cooldown runs out by wall-clock.
func TestChannelHealth_SuccessDoesNotCancelActiveCooldown(t *testing.T) {
	resetChannelHealth(t, time.Second, 200*time.Millisecond, 5)
	const ch = 9

	for i := 0; i < 5; i++ {
		RecordChannelFailure(ch)
	}
	if !IsChannelAffinityUnhealthy(ch) {
		t.Fatalf("channel should be unhealthy after threshold")
	}
	RecordChannelSuccess(ch) // sporadic success mid-cooldown
	if !IsChannelAffinityUnhealthy(ch) {
		t.Fatalf("a single success must NOT cancel an active cooldown (anti-flap)")
	}
}

func TestChannelHealth_CooldownExpiresByTime(t *testing.T) {
	resetChannelHealth(t, time.Second, 80*time.Millisecond, 5)
	const ch = 10

	for i := 0; i < 5; i++ {
		RecordChannelFailure(ch)
	}
	if !IsChannelAffinityUnhealthy(ch) {
		t.Fatalf("channel should be unhealthy after threshold")
	}
	time.Sleep(120 * time.Millisecond)
	if IsChannelAffinityUnhealthy(ch) {
		t.Fatalf("cooldown should have expired by wall-clock")
	}
}

func TestChannelHealth_ZeroChannelIDNoop(t *testing.T) {
	resetChannelHealth(t, time.Second, time.Second, 1)
	RecordChannelFailure(0)
	if IsChannelAffinityUnhealthy(0) {
		t.Fatalf("channel id 0 must never be considered unhealthy")
	}
}
