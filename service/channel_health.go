package service

import (
	"sync"
	"time"
)

// Channel affinity health tracking.
//
// Channel affinity pins a request stream (by prompt_cache_key etc.) to a single
// upstream channel to keep the upstream prompt cache warm. The downside: if that
// pinned channel's upstream starts failing (e.g. 502 for minutes), every retry
// keeps resolving back to the same dead channel and a single user can be stuck on
// errors while healthy channels serve everyone else.
//
// This tracker gives the affinity gate a real-time health signal: when a channel
// accumulates failureThreshold failures within failWindow, it enters a cooldown
// during which the distributor will NOT honor an affinity pin to it (the request
// falls through to normal weighted selection, which lands on a healthy channel).
//
// Semantics chosen deliberately:
//   - The gate is fail-open: an unhealthy channel is only dropped from *affinity
//     pinning*, never from normal channel selection. So this can never reduce
//     availability below the no-affinity baseline.
//   - A success clears the pre-trip failure accumulation (a healthy channel never
//     trips) but does NOT cancel an already-active cooldown. The cooldown runs its
//     full duration by wall-clock. This avoids flapping on half-failing channels
//     where sporadic successes are interleaved with errors.
// Tunable via package vars (kept as vars rather than consts so tests can use
// short windows and ops could later promote them to env settings).
var (
	channelAffinityFailWindow    = 30 * time.Second
	channelAffinityFailThreshold = 5
	channelAffinityCooldown      = 60 * time.Second
)

type channelHealthState struct {
	failTimes   []time.Time
	cooldownTil time.Time
}

var (
	channelHealthMu sync.Mutex
	channelHealth   = make(map[int]*channelHealthState)
)

// RecordChannelFailure records a channel-side failure (5xx / timeout / network).
// Called per failing attempt, so a heavily-retried request accumulates quickly.
func RecordChannelFailure(channelID int) {
	if channelID <= 0 {
		return
	}
	now := time.Now()
	channelHealthMu.Lock()
	defer channelHealthMu.Unlock()
	st := channelHealth[channelID]
	if st == nil {
		st = &channelHealthState{}
		channelHealth[channelID] = st
	}
	// Drop failures that fell out of the sliding window, then append this one.
	cutoff := now.Add(-channelAffinityFailWindow)
	kept := st.failTimes[:0]
	for _, t := range st.failTimes {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	st.failTimes = append(kept, now)
	if len(st.failTimes) >= channelAffinityFailThreshold {
		// Trip (or extend) the cooldown and reset the window counter.
		st.cooldownTil = now.Add(channelAffinityCooldown)
		st.failTimes = st.failTimes[:0]
	}
}

// RecordChannelSuccess clears the pre-trip failure accumulation for a channel.
// It intentionally does NOT cancel an active cooldown — the cooldown expires by
// time so a half-failing channel cannot be un-cooled by a single lucky success.
func RecordChannelSuccess(channelID int) {
	if channelID <= 0 {
		return
	}
	channelHealthMu.Lock()
	defer channelHealthMu.Unlock()
	if st := channelHealth[channelID]; st != nil {
		st.failTimes = st.failTimes[:0]
	}
}

// IsChannelAffinityUnhealthy reports whether the channel is currently in an
// affinity cooldown. Used only to skip affinity pinning, never to remove the
// channel from normal selection.
func IsChannelAffinityUnhealthy(channelID int) bool {
	if channelID <= 0 {
		return false
	}
	channelHealthMu.Lock()
	defer channelHealthMu.Unlock()
	st := channelHealth[channelID]
	return st != nil && time.Now().Before(st.cooldownTil)
}
