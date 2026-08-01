package engine

import (
	"math"
	"runtime"
	"sync/atomic"
	"time"
)

// AdaptiveOptimizer continuously monitors execution metrics and automatically
// tunes batch sizes and flush intervals in real-time.
type AdaptiveOptimizer struct {
	reqCount          uint64
	lastReqCount      uint64
	currentRps        uint64 // atomic math.Float64bits
	lastCheck         time.Time

	batchSize         uint32
	flushIntervalNano int64
	mode              unsafeAtomicString
	stopCh            chan struct{}
}

type unsafeAtomicString struct {
	val atomic.Value
}

func (s *unsafeAtomicString) Store(v string) {
	s.val.Store(v)
}

func (s *unsafeAtomicString) Load() string {
	if v := s.val.Load(); v != nil {
		return v.(string)
	}
	return "Micro-Latency"
}

// NewAdaptiveOptimizer creates and starts a self-improving latency optimizer.
func NewAdaptiveOptimizer() *AdaptiveOptimizer {
	opt := &AdaptiveOptimizer{
		lastCheck:         time.Now(),
		batchSize:         500,
		flushIntervalNano: int64(1 * time.Millisecond),
		stopCh:            make(chan struct{}),
	}
	opt.mode.Store("Micro-Latency")
	go opt.tuneLoop()
	return opt
}

// Close stops the adaptive optimizer background loop.
func (o *AdaptiveOptimizer) Close() {
	select {
	case <-o.stopCh:
	default:
		close(o.stopCh)
	}
}

// RecordRequest increments the request sampler.
func (o *AdaptiveOptimizer) RecordRequest() {
	atomic.AddUint64(&o.reqCount, 1)
}

// GetBatchSize returns the current dynamically optimized batch size.
func (o *AdaptiveOptimizer) GetBatchSize() int {
	return int(atomic.LoadUint32(&o.batchSize))
}

// GetFlushInterval returns the current optimized flush interval.
func (o *AdaptiveOptimizer) GetFlushInterval() time.Duration {
	return time.Duration(atomic.LoadInt64(&o.flushIntervalNano))
}

// GetRPS returns the current calculated requests per second.
func (o *AdaptiveOptimizer) GetRPS() float64 {
	bits := atomic.LoadUint64(&o.currentRps)
	return math.Float64frombits(bits)
}

// GetMode returns the current active optimization mode name.
func (o *AdaptiveOptimizer) GetMode() string {
	return o.mode.Load()
}

// GetShardCount returns the number of write shards, which scales with runtime.NumCPU().
// Capped between 4 and 16 to balance parallelism and SQLite WAL contention.
func (o *AdaptiveOptimizer) GetShardCount() int {
	n := runtime.NumCPU()
	if n < 4 {
		n = 4
	}
	if n > 16 {
		n = 16
	}
	return n
}

func (o *AdaptiveOptimizer) tuneLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-o.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			currentReqs := atomic.LoadUint64(&o.reqCount)
			elapsed := now.Sub(o.lastCheck).Seconds()

			if elapsed <= 0 {
				continue
			}

			delta := currentReqs - o.lastReqCount
			rps := float64(delta) / elapsed
			atomic.StoreUint64(&o.currentRps, math.Float64bits(rps))

			o.lastReqCount = currentReqs
			o.lastCheck = now

			// Autonomous tuning decisions based on live RPS
			switch {
			case rps > 20000:
				atomic.StoreUint32(&o.batchSize, 10000)
				atomic.StoreInt64(&o.flushIntervalNano, int64(250*time.Microsecond))
				o.mode.Store("Extreme-Batching")

			case rps > 10000:
				atomic.StoreUint32(&o.batchSize, 5000)
				atomic.StoreInt64(&o.flushIntervalNano, int64(500*time.Microsecond))
				o.mode.Store("Aggressive-Batching")

			case rps > 2000:
				atomic.StoreUint32(&o.batchSize, 2500)
				atomic.StoreInt64(&o.flushIntervalNano, int64(1*time.Millisecond))
				o.mode.Store("High-Throughput")

			case rps > 200:
				atomic.StoreUint32(&o.batchSize, 1000)
				atomic.StoreInt64(&o.flushIntervalNano, int64(2*time.Millisecond))
				o.mode.Store("Balanced")

			default:
				atomic.StoreUint32(&o.batchSize, 250)
				atomic.StoreInt64(&o.flushIntervalNano, int64(5*time.Millisecond))
				o.mode.Store("Micro-Latency")
			}
		}
	}
}
