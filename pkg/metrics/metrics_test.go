package metrics

import (
	"testing"
	"time"
)

func TestMetricsSampling(t *testing.T) {
	mon := GetMonitor()
	m1 := mon.Sample()

	if m1.WorkingSetMB <= 0 {
		t.Errorf("Expected positive WorkingSetMB, got %.2f", m1.WorkingSetMB)
	}

	if m1.Summary == "" {
		t.Errorf("Expected non-empty summary string")
	}

	// Burn a tiny bit of CPU to see delta
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 1000000; i++ {
		_ = i * 2
	}

	m2 := mon.Sample()
	if m2.WorkingSetMB <= 0 {
		t.Errorf("Expected positive WorkingSetMB on second sample, got %.2f", m2.WorkingSetMB)
	}

	t.Logf("Sample 1: %s (%s)", m1.Summary, m1.Detail)
	t.Logf("Sample 2: %s (%s)", m2.Summary, m2.Detail)
}
