package spine

import (
	"sync/atomic"
	"time"
)

// AdaptiveOptimizer continuously monitors execution metrics and automatically
// tunes batch sizes and flush intervals in real-time.
type AdaptiveOptimizer struct {
	reqCount     uint64
	lastReqCount uint64
	lastCheck    time.Time

	batchSize     uint32
	flushInterval time.Duration
	mode          unsafeAtomicString
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
		lastCheck:     time.Now(),
		batchSize:     500,
		flushInterval: 1 * time.Millisecond,
	}
	opt.mode.Store("Micro-Latency")
	go opt.tuneLoop()
	return opt
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
	return o.flushInterval
}

// GetMode returns the current active optimization mode name.
func (o *AdaptiveOptimizer) GetMode() string {
	return o.mode.Load()
}

func (o *AdaptiveOptimizer) tuneLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		currentReqs := atomic.LoadUint64(&o.reqCount)
		elapsed := now.Sub(o.lastCheck).Seconds()

		if elapsed <= 0 {
			continue
		}

		delta := currentReqs - o.lastReqCount
		rps := float64(delta) / elapsed

		o.lastReqCount = currentReqs
		o.lastCheck = now

		// Autonomous tuning decisions based on live RPS
		switch {
		case rps > 20000:
			atomic.StoreUint32(&o.batchSize, 10000)
			o.flushInterval = 250 * time.Microsecond
			o.mode.Store("Extreme-Batching")

		case rps > 10000:
			atomic.StoreUint32(&o.batchSize, 5000)
			o.flushInterval = 500 * time.Microsecond
			o.mode.Store("Aggressive-Batching")

		case rps > 2000:
			atomic.StoreUint32(&o.batchSize, 2500)
			o.flushInterval = 1 * time.Millisecond
			o.mode.Store("High-Throughput")

		case rps > 200:
			atomic.StoreUint32(&o.batchSize, 1000)
			o.flushInterval = 2 * time.Millisecond
			o.mode.Store("Balanced")

		default:
			atomic.StoreUint32(&o.batchSize, 250)
			o.flushInterval = 5 * time.Millisecond
			o.mode.Store("Micro-Latency")
		}
	}
}
