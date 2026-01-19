package traffic

import (
	"hash/fnv"
	"sync/atomic"
	"time"
)

type Variant string

const (
	VariantStable Variant = "stable"
	VariantCanary Variant = "canary"
)

type Split struct {
	StableWeight int
	CanaryWeight int
}

var rngSeed uint64 = uint64(time.Now().UnixNano())

func SetSeedForTests(seed uint64) {
	if seed == 0 {
		seed = 1
	}
	atomic.StoreUint64(&rngSeed, seed)
}

func (s Split) ChooseRandom() Variant {
	return s.choose(nextRand())
}

func (s Split) ChooseDeterministic(key string) Variant {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return s.choose(h.Sum64())
}

func (s Split) choose(value uint64) Variant {
	total := s.StableWeight + s.CanaryWeight
	if total <= 0 || s.CanaryWeight <= 0 {
		return VariantStable
	}
	if s.StableWeight <= 0 {
		return VariantCanary
	}
	roll := int(value % uint64(total))
	if roll < s.CanaryWeight {
		return VariantCanary
	}
	return VariantStable
}

func nextRand() uint64 {
	for {
		seed := atomic.LoadUint64(&rngSeed)
		if seed == 0 {
			seed = 1
		}
		x := seed
		x ^= x >> 12
		x ^= x << 25
		x ^= x >> 27
		x *= 2685821657736338717
		if atomic.CompareAndSwapUint64(&rngSeed, seed, x) {
			return x
		}
	}
}
