package gpu

import (
	"testing"
)

func TestGPUBufferAllocation(t *testing.T) {
	buf, err := NewGPUBuffer(1000)
	if err != nil {
		t.Fatalf("Failed to create GPUBuffer: %v", err)
	}

	if buf.Size() < 1000 {
		t.Errorf("Expected size at least 1000, got %d", buf.Size())
	}

	if buf.Size()%PageSize != 0 {
		t.Errorf("Buffer size %d must be page-aligned to %d", buf.Size(), PageSize)
	}

	if buf.HostPointer() == 0 {
		t.Errorf("Host memory pointer must not be 0")
	}

	if buf.DevicePointer() == 0 {
		t.Errorf("Device VRAM pointer must not be 0")
	}
}

func TestGPUWriteAndPCIeTransfer(t *testing.T) {
	buf, err := NewGPUBuffer(4096)
	if err != nil {
		t.Fatalf("NewGPUBuffer failed: %v", err)
	}

	payload := []byte("gpu_event_payload_test_data")
	offset, err := buf.WriteEvent(payload)
	if err != nil {
		t.Fatalf("WriteEvent failed: %v", err)
	}
	if offset != 0 {
		t.Errorf("Expected initial offset 0, got %d", offset)
	}

	// Copy to Device (H2D)
	copied, _, err := buf.CopyToDevice()
	if err != nil {
		t.Fatalf("CopyToDevice failed: %v", err)
	}
	if copied != uint64(len(payload)) {
		t.Errorf("Expected %d bytes copied to device, got %d", len(payload), copied)
	}

	// Copy from Device (D2H)
	out, _, err := buf.CopyFromDevice()
	if err != nil {
		t.Fatalf("CopyFromDevice failed: %v", err)
	}
	if string(out) != string(payload) {
		t.Errorf("Expected payload '%s', got '%s'", string(payload), string(out))
	}

	stats := buf.Stats()
	if stats["pcie_h2d_transfers"] != uint64(1) {
		t.Errorf("Expected 1 H2D transfer, got %v", stats["pcie_h2d_transfers"])
	}
	if stats["pcie_d2h_transfers"] != uint64(1) {
		t.Errorf("Expected 1 D2H transfer, got %v", stats["pcie_d2h_transfers"])
	}
}

func TestGPUComputeEngineBatch(t *testing.T) {
	engine := NewGPUComputeEngine(BackendCUDA)
	if engine.Backend() != BackendCUDA {
		t.Errorf("Expected CUDA backend, got %s", engine.Backend())
	}

	buf, err := NewGPUBuffer(16384)
	if err != nil {
		t.Fatalf("NewGPUBuffer failed: %v", err)
	}

	for i := 0; i < 500; i++ {
		_, _ = buf.WriteEvent([]byte("event_data_item"))
	}

	processed, elapsed, err := engine.ProcessBatch(buf, 500)
	if err != nil {
		t.Fatalf("ProcessBatch failed: %v", err)
	}
	if processed != 500 {
		t.Errorf("Expected 500 events processed, got %d", processed)
	}
	if elapsed <= 0 {
		t.Errorf("Expected positive execution duration, got %d", elapsed)
	}

	caps := engine.HWCapabilities()
	if caps["backend"] != BackendCUDA {
		t.Errorf("Expected backend CUDA in HWCapabilities, got %v", caps["backend"])
	}
	if caps["unified_memory"] != false {
		t.Errorf("Discrete GPU must specify unified_memory=false")
	}
}
