package matching

import (
	"strconv"
	"testing"
)

// TestShuffleWithinTiersPreservesTierBoundaries verifies the core invariant:
// after shuffling, every element originally in tier N is still in tier N.
// If this ever fails, the shuffle is no longer preserving nearest-first
// semantics and the fix is silently regressed.
func TestShuffleWithinTiersPreservesTierBoundaries(t *testing.T) {
	const tierSize = 10
	const n = 20

	// Original order encodes the nearest-first index in each element,
	// so we can check tier membership after shuffling.
	drivers := make([]string, n)
	for i := 0; i < n; i++ {
		drivers[i] = "driver-" + strconv.Itoa(i)
	}

	// Run many trials so any tier-crossing bug is very likely to surface.
	for trial := 0; trial < 200; trial++ {
		copy := append([]string(nil), drivers...)
		shuffleWithinTiers(copy, tierSize)

		// Tier 1 (indices 0..9) must contain the driver-0 through driver-9 set.
		tier1 := stringSet(copy[:tierSize])
		for i := 0; i < tierSize; i++ {
			want := "driver-" + strconv.Itoa(i)
			if _, ok := tier1[want]; !ok {
				t.Fatalf("trial %d: %s escaped tier 1: got %v", trial, want, copy[:tierSize])
			}
		}

		// Tier 2 (indices 10..19) must contain the driver-10 through driver-19 set.
		tier2 := stringSet(copy[tierSize:])
		for i := tierSize; i < n; i++ {
			want := "driver-" + strconv.Itoa(i)
			if _, ok := tier2[want]; !ok {
				t.Fatalf("trial %d: %s escaped tier 2: got %v", trial, want, copy[tierSize:])
			}
		}
	}
}

// TestShuffleWithinTiersActuallyShuffles guards against the tier size being
// so large (or so small) that the "shuffle" is effectively identity. If the
// output equals the input across many trials, we've silently lost the
// contention-spreading property.
func TestShuffleWithinTiersActuallyShuffles(t *testing.T) {
	const tierSize = 10
	const n = 20

	drivers := make([]string, n)
	for i := 0; i < n; i++ {
		drivers[i] = "driver-" + strconv.Itoa(i)
	}

	permutedAtLeastOnce := false
	for trial := 0; trial < 100; trial++ {
		copy := append([]string(nil), drivers...)
		shuffleWithinTiers(copy, tierSize)
		if !equal(copy, drivers) {
			permutedAtLeastOnce = true
			break
		}
	}
	if !permutedAtLeastOnce {
		t.Fatalf("shuffleWithinTiers produced identity in 100 trials — is it actually shuffling?")
	}
}

// TestShuffleWithinTiersHandlesRaggedLastTier verifies the last (short)
// tier when len(drivers) is not a clean multiple of tierSize.
func TestShuffleWithinTiersHandlesRaggedLastTier(t *testing.T) {
	drivers := []string{"a", "b", "c", "d", "e", "f", "g"} // 7 items
	shuffleWithinTiers(drivers, 5)                          // → tiers of 5 and 2

	seen := stringSet(drivers)
	for _, want := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("element %q missing after shuffle: %v", want, drivers)
		}
	}
}

// TestShuffleWithinTiersTrivialTierSize verifies tierSize < 2 is a no-op,
// so we don't accidentally cause a "shuffle 1-element slice" pointless
// call in a hot path if someone tunes tierSize down.
func TestShuffleWithinTiersTrivialTierSize(t *testing.T) {
	drivers := []string{"a", "b", "c"}
	shuffleWithinTiers(drivers, 1)
	if !equal(drivers, []string{"a", "b", "c"}) {
		t.Fatalf("tierSize=1 should be a no-op, got %v", drivers)
	}
	shuffleWithinTiers(drivers, 0)
	if !equal(drivers, []string{"a", "b", "c"}) {
		t.Fatalf("tierSize=0 should be a no-op, got %v", drivers)
	}
}

func stringSet(s []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, v := range s {
		out[v] = struct{}{}
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
