package hivemind

// TestLiveRollingKeysAboveAllISaYu proves the rolling keys the running
// mesh actually uses: derive "now" + "next hour" + "today + tomorrow"
// from the same code path gaze reads, so the dashboard's hour_key and
// day_root are provably rotating, not decorative.

import (
	"testing"
	"time"
)

func TestLiveRollingKeysAboveAllISaYu(t *testing.T) {
	// The seeded path: relay key baked at build time selects HMAC-backed
	// leaves. Force a deterministic seed so the test is a real derivation.
	const seed = "ab94f253510372b0cee7c871ba7c5d3fea4b20caeb0d271de02860f739d3e5c1"

	now := time.Now()
	nowHour := hourEpoch(now)
	day := nowHour / 24

	cur, ok := ratchetKey(seed, "proof|box", "probe", nowHour)
	if !ok {
		t.Fatal("current hour key not derivable")
	}
	next, ok := ratchetKey(seed, "proof|box", "probe", nowHour+1)
	if !ok {
		t.Fatal("next hour key not derivable")
	}
	prev, ok := ratchetKey(seed, "proof|box", "probe", nowHour-1)
	if !ok {
		t.Fatal("previous hour key not derivable")
	}
	if string(cur) == string(next) || string(cur) == string(prev) {
		t.Fatal("hour keys do not rotate hour-to-hour")
	}

	today, ok := dayKey(seed, "proof|box", "probe", day)
	if !ok {
		t.Fatal("today root not derivable")
	}
	tomorrow, ok := dayKey(seed, "proof|box", "probe", day+1)
	if !ok {
		t.Fatal("tomorrow root not derivable")
	}
	if string(today) == string(tomorrow) {
		t.Fatal("TOTD roots do not roll day-to-day")
	}

	// Cross-hour replay armor: AAD must differ hour to hour, so an old
	// frame cannot pass under the new hour.
	if hourAAD(nowHour) == hourAAD(nowHour-1) {
		t.Fatal("AAD identical across hour — replay armor ineffective")
	}

	t.Logf("hour %d:  %x", nowHour, cur[:8])
	t.Logf("hour %d:  %x (next, differs)", nowHour+1, next[:8])
	t.Logf("day %d root: %x", day, today[:8])
	t.Logf("day %d root: %x (tommorow, differs)", day+1, tomorrow[:8])
	t.Logf("rotation + replay armor proven on the same code path gaze reads")
}
