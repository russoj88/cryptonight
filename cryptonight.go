// HEAD_PLACEHOLDER
// +build ignore

// Package cryptonight implements CryptoNight hash function and some of its
// variant.
//
// ref: https://cryptonote.org/cns/cns008.txt
package cryptonight // import "ekyu.moe/cryptonight"

import (
	"hash"
	"runtime"
	"unsafe"

	"github.com/aead/skein"
	"github.com/dchest/blake256"

	"ekyu.moe/cryptonight/groestl"
	"ekyu.moe/cryptonight/internal/aes"
	"ekyu.moe/cryptonight/internal/sha3"
	"ekyu.moe/cryptonight/jh"
)

// This field is for macro definitions.
// We define it in a literal string so that it can trick gofmt(1).
//
// It should be empty after they are expanded by cpp(1).
const _ = `
#undef build
#undef ignore

#define U64_U8(a, begin, end) \
	((*[((end) - (begin)) * 8]uint8)(unsafe.Pointer(&a[begin])))

#define U8_U32(a, begin, end) \
	((*[((end) - (begin)) / 4]uint32)(unsafe.Pointer(&a[begin])))

#define TO_ADDR(a) \
	((uint32(a[2])<<16 | uint32(a[1])<<8 | uint32(a[0])) & 0x1ffff0)
`

// To trick goimports(1).
var _ = unsafe.Pointer(nil)

// Cache can reuse the memory chunks for potential multiple Sum calls. A Cache
// instance occupies 2,097,352 bytes in memory.
//
// cache.Sum is not concurrent safe. A Cache only allows at most one Sum running.
// If you intend to call cache.Sum it concurrently, you should either create
// multiple Cache instances (recommended for mining apps), or use a sync.Pool to
// manage multiple Cache instances (recommended for mining pools).
//
//
// Example for multiple instances (mining app):
//		n := runtime.GOMAXPROCS()
//		c := make([]*cryptonight.Cached, n)
//		for i := 0; i < n; i++ {
//			c[i] = new(cryptonight.Cached)
//		}
//
//		// ...
//		for _, v := range c {
//			go func() {
//				for {
//					sum := v.Sum(data, 1)
//					// do something with sum...
//				}
//			}()
//		}
//		// ...
//
//
// Example for sync.Pool (mining pool):
//		cachePool := sync.Pool{
//			New: func() interface{} {
//				return new(cryptonight.Cache)
//			},
//		}
//
//		// ...
//		data := <-share // received from some miner
//		cache := cachePool.Get().(*cryptonight.Cache)
//		sum := cache.Sum(data, 1)
//		cachePool.Put(cache) // a Cache is not used after Sum.
//		// do something with sum...
//
// The zero value for Cache is ready to use.
type Cache struct {
	finalState [200]byte
	scratchpad [2 * 1024 * 1024]byte
}

// Sum calculate a CryptoNight hash digest. The return value is exactly 32 bytes
// long.
//
// Note that if variant is 1, then data is required to have at least 43 bytes.
// This is assumed and not checked by Sum. If such condition doesn't meet, Sum
// will panic.
func (cache *Cache) Sum(data []byte, variant int) []byte {
	// as per cns008 sec.3 Scratchpad Initialization
	sha3.Keccak1600State(&cache.finalState, data)

	tweak := make([]byte, 8)
	if variant == 1 {
		// therefore data must be larger than 43 bytes
		xorWords(tweak, cache.finalState[192:], data[35:43])
	}

	aesKey := cache.finalState[:32]
	rkeys := make([]uint32, 10*4) // 10 rounds, instead of 14 as in standard AES-256
	aes.CnExpandKey(aesKey, rkeys)
	blocks := make([]byte, 128)
	copy(blocks, cache.finalState[64:192])

	for j := 0; j < 2*1024*1024; j += 128 {
		for i := 0; i < 128; i += 16 {
			aes.CnRounds(blocks[i:], blocks[i:], rkeys)
		}
		copy(cache.scratchpad[j:], blocks)
	}

	// as per cns008 sec.4 Memory-Hard Loop
	a64, b64 := new([2]uint64), new([2]uint64)
	c64, d64 := new([2]uint64), new([2]uint64)
	a8, b8 := U64_U8(a64, 0, 2), U64_U8(b64, 0, 2) // same pointer, but different layout
	c8, d8 := U64_U8(c64, 0, 2), U64_U8(d64, 0, 2)
	product := new([2]uint64)
	rk := new([4]uint32)
	var addr uint32

	xorWords(a8[:], cache.finalState[:16], cache.finalState[32:48])
	xorWords(b8[:], cache.finalState[16:32], cache.finalState[48:64])

	for i := 0; i < 524288; i++ {
		addr = TO_ADDR(a8)
		rk = U8_U32(a8, 0, 16)
		aes.CnSingleRound(c8[:], cache.scratchpad[addr:], rk[:])
		xorWords(cache.scratchpad[addr:], b8[:], c8[:])
		copy(b64[:], c64[:])

		if variant == 1 {
			t := cache.scratchpad[addr+11]
			t = ((^t)&1)<<4 | (((^t)&1)<<4&t)<<1 | (t&32)>>1
			cache.scratchpad[addr+11] ^= t
		}

		addr = TO_ADDR(c8)
		copy(d8[:], cache.scratchpad[addr:])
		byteMul(product, c64[0], d64[0])
		// byteAdd
		a64[0] += product[0]
		a64[1] += product[1]

		copy(cache.scratchpad[addr:], a8[:])
		xorWords(a8[:], a8[:], d8[:])

		if variant == 1 {
			for i := uint32(0); i < 8; i++ {
				cache.scratchpad[addr+i+8] ^= tweak[i]
			}
		}
	}

	// as per cns008 sec.5 Result Calculation
	aesKey = cache.finalState[32:64]
	aes.CnExpandKey(aesKey, rkeys)
	blocks = cache.finalState[64:192]

	for j := 0; j < 2*1024*1024; j += 128 {
		xorWords(cache.scratchpad[j:j+128], cache.scratchpad[j:j+128], blocks)
		for i := 0; i < 128; i += 16 {
			aes.CnRounds(cache.scratchpad[j+i:j+i+16], cache.scratchpad[j+i:j+i+16], rkeys)
		}
		blocks = cache.scratchpad[j : j+128]
	}

	copy(cache.finalState[64:192], blocks)

	// This KeepAlive is a must, as we hacked too much for memory.
	runtime.KeepAlive(cache.finalState)
	sha3.Keccak1600Permute(&cache.finalState)

	var h hash.Hash
	switch cache.finalState[0] & 0x03 {
	case 0x00:
		h = blake256.New()
	case 0x01:
		h = groestl.New256()
	case 0x02:
		h = jh.New256()
	default:
		h = skein.New256(nil)
	}
	h.Write(cache.finalState[:])

	return h.Sum(nil)
}

// Sum calculate a CryptoNight hash digest. The return value is exactly 32 bytes
// long.
//
// Note that if variant is 1, then data is required to have at least 43 bytes.
// This is assumed and not checked by Sum. If such condition doesn't meet, Sum
// will panic.
//
// Sum is not recommended for a large scale of calls as it consumes a large
// amount of memory. In such scenario, consider using Cache instead.
func Sum(data []byte, variant int) []byte {
	return new(Cache).Sum(data, variant)
}
