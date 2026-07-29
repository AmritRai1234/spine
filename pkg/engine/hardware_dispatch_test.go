package engine

import (
	"testing"
)

func TestHardwareDispatcherResolution(t *testing.T) {
	dispatcher := NewHardwareDispatcher()

	// Test 1: Explicit GPU policy
	gpuRes := dispatcher.ResolveEngine("AIEmbeddingNode", "gpu")
	if gpuRes.ConfiguredPolicy != ComputeGPU {
		t.Errorf("Expected ComputeGPU policy, got %s", gpuRes.ConfiguredPolicy)
	}
	if gpuRes.UnifiedMemory {
		t.Errorf("Discrete GPU should not be marked as UnifiedMemory")
	}

	// Test 2: Explicit APU policy
	apuRes := dispatcher.ResolveEngine("BatchCryptoNode", "apu")
	if apuRes.ConfiguredPolicy != ComputeAPU {
		t.Errorf("Expected ComputeAPU policy, got %s", apuRes.ConfiguredPolicy)
	}
	if !apuRes.UnifiedMemory {
		t.Errorf("APU engine must be marked as UnifiedMemory")
	}

	// Test 3: Explicit CPU policy
	cpuRes := dispatcher.ResolveEngine("LoggingNode", "cpu")
	if cpuRes.ConfiguredPolicy != ComputeCPU {
		t.Errorf("Expected ComputeCPU policy, got %s", cpuRes.ConfiguredPolicy)
	}

	// Test 4: Auto policy resolution
	autoRes := dispatcher.ResolveEngine("DefaultNode", "auto")
	if autoRes.ConfiguredPolicy != ComputeAuto {
		t.Errorf("Expected ComputeAuto policy, got %s", autoRes.ConfiguredPolicy)
	}
	if !autoRes.UnifiedMemory {
		t.Errorf("Auto policy on APU host should auto-select UnifiedMemory")
	}
}
