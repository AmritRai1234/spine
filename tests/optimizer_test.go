package tests

import (
	"testing"
	"time"

	"github.com/AmritRai1234/spine/pkg/engine"
)

func TestAdaptiveOptimizer(t *testing.T) {
	opt := engine.NewAdaptiveOptimizer()

	// Initial mode should be Micro-Latency
	if mode := opt.GetMode(); mode != "Micro-Latency" {
		t.Errorf("expected initial mode Micro-Latency, got %s", mode)
	}

	// Simulate 15000 requests to trigger Aggressive-Batching mode
	for i := 0; i < 15000; i++ {
		opt.RecordRequest()
	}

	// Wait for tune loop interval
	time.Sleep(150 * time.Millisecond)

	if batchSize := opt.GetBatchSize(); batchSize < 1000 {
		t.Errorf("expected batch size >= 1000 under high load, got %d", batchSize)
	}

	if mode := opt.GetMode(); mode != "Extreme-Batching" && mode != "Aggressive-Batching" && mode != "High-Throughput" {
		t.Errorf("expected Extreme-Batching, Aggressive-Batching or High-Throughput mode, got %s", mode)
	}
}
