package main

import "testing"

func TestParseBenchmarkOutput(t *testing.T) {
	raw := "BenchmarkExample-10 123 456.7 ns/op 8192 B/op 12 allocs/op\n"
	ns, bytesOp, allocsOp, ok := parseBenchmarkOutput(raw)
	if !ok || ns != 456.7 || bytesOp != 8192 || allocsOp != 12 {
		t.Fatalf("parsed benchmark = %v %v %v %v", ns, bytesOp, allocsOp, ok)
	}
}

func TestParseBenchmarkOutputRejectsMissingMetric(t *testing.T) {
	if _, _, _, ok := parseBenchmarkOutput("PASS\n"); ok {
		t.Fatal("missing benchmark output was accepted")
	}
}
