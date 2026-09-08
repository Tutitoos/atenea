package main

import "testing"

func TestSummarizeUsesOnlySuccessfulSamples(t *testing.T) {
	op := operation{Samples: []sample{{DurationMS: 10, Bytes: 100, Outcome: "observed"}, {DurationMS: 20, Bytes: 200, Outcome: "observed"}, {DurationMS: 1, Outcome: "failed"}}}
	summarize(&op)
	if op.MedianMS != 10 || op.P95MS != 10 || op.MedianB != 100 {
		t.Fatalf("summary = %#v", op)
	}
}
