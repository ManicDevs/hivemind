package fabricsim

import (
	"math"
	"sync"
)

// Rand is a deterministic, allocation-free pseudo-random generator (xoshiro
// 256**). The simulator uses it instead of math/rand for two reasons: it is
// safe for concurrent use without a global lock, and a seeded run is exactly
// reproducible, which is what makes a failing adversarial run debuggable.
type Rand struct {
	mu    sync.Mutex
	s     [4]uint64
	seedN uint64
}

// NewRand returns a generator seeded with the given value. Two simulators built
// with the same seed produce byte-identical traffic.
func NewRand(seed uint64) *Rand {
	r := &Rand{}
	r.Seed(seed)
	return r
}

// Seed reinitialises the generator state from a 64-bit seed using SplitMix64,
// which decorrelates the initial state from small sequential seeds.
func (r *Rand) Seed(seed uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seedN = seed
	x := seed
	for i := range r.s {
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		r.s[i] = z ^ (z >> 31)
	}
	// An all-zero state is a fixed point of xoshiro; avoid it.
	if r.s[0]|r.s[1]|r.s[2]|r.s[3] == 0 {
		r.s[0] = 0x9e3779b97f4a7c15
	}
}

// next returns the next 64-bit value. Callers must hold r.mu.
func (r *Rand) next() uint64 {
	result := r.s[1]*5 + 1
	t := r.s[1] << 17
	r.s[2] ^= r.s[0]
	r.s[3] ^= r.s[1]
	r.s[1] ^= r.s[2]
	r.s[0] ^= r.s[3]
	r.s[2] ^= t
	r.s[3] = r.s[3]<<45 | r.s[3]>>19
	return result
}

// Uint64 returns a uniformly distributed 64-bit value.
func (r *Rand) Uint64() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.next()
}

// Intn returns a uniformly distributed value in [0,n). It panics if n <= 0,
// matching math/rand's contract.
func (r *Rand) Intn(n int) int {
	if n <= 0 {
		panic("fabricsim: Intn requires n > 0")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Unbiased rejection sampling: discard the short tail of the range so
	// every value is equally likely. With n small and 64 bits of entropy the
	// rejection probability is negligible, but the bias would be a real
	// fairness bug in the attacker's targeting.
	limit := uint64(-int64(n)) % uint64(n) // 2^64 mod n
	for {
		v := r.next()
		if v >= limit {
			return int(v % uint64(n))
		}
	}
}

// Float64 returns a uniformly distributed value in [0,1).
func (r *Rand) Float64() float64 {
	return float64(r.Uint64()>>11) / float64(1<<53)
}

// Chance reports whether a 1-in-1-in-n event occurred, for Bernoulli choices.
func (r *Rand) Chance(n int) bool {
	if n <= 1 {
		return true
	}
	return r.Intn(n) == 0
}

// Range returns a uniformly distributed value in [lo,hi].
func (r *Rand) Range(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + r.Intn(hi-lo+1)
}

// NormalZ returns a standard-normal deviate via Box-Muller. It is used for
// statistical anomaly scoring, where a Gaussian noise model is what the
// detector is actually testing against.
func (r *Rand) NormalZ() float64 {
	u1 := r.Float64()
	if u1 < 1e-12 {
		u1 = 1e-12
	}
	u2 := r.Float64()
	// sqrt(-2 ln u1) * cos(2 pi u2)
	return math.Sqrt(-2.0*math.Log(u1)) * math.Cos(2.0*math.Pi*u2)
}
