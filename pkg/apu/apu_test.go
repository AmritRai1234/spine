package apu

import (
	"testing"
)

func TestAPUBuffer(t *testing.T) {
	buf, err := NewAPUBuffer(1024 * 64)
	if err != nil {
		t.Fatalf("Failed to create APU buffer: %v", err)
	}

	if buf.Size() < 64*1024 {
		t.Errorf("Expected buffer size >= 64KB, got %d", buf.Size())
	}

	payload := []byte("hello_apu_unified_memory")
	offset, err := buf.WriteEvent(payload)
	if err != nil {
		t.Fatalf("WriteEvent failed: %v", err)
	}

	readData, err := buf.ReadSlice(offset, len(payload))
	if err != nil {
		t.Fatalf("ReadSlice failed: %v", err)
	}

	if string(readData) != string(payload) {
		t.Errorf("Expected %s, got %s", string(payload), string(readData))
	}

	stats := buf.Stats()
	if stats["pcie_bus_copies"] != 0 {
		t.Errorf("Expected 0 PCIe copies for APU, got %v", stats["pcie_bus_copies"])
	}
}

func TestAPUBenchmark(t *testing.T) {
	suite, err := NewBenchmarkSuite(1024 * 1024)
	if err != nil {
		t.Fatalf("Failed to create benchmark suite: %v", err)
	}

	metrics, err := suite.RunBenchmark(10000)
	if err != nil {
		t.Fatalf("RunBenchmark failed: %v", err)
	}

	if len(metrics) != 3 {
		t.Fatalf("Expected 3 metrics, got %d", len(metrics))
	}

	// APU metric should be the 3rd metric
	apuMetric := metrics[2]
	if apuMetric.PCIeCopiedBytes != 0 {
		t.Errorf("Expected APU PCIe copied bytes = 0, got %d", apuMetric.PCIeCopiedBytes)
	}
}
