package main

import (
	"slices"
	"time"
)

// rateTolerance is the fraction of Target.Rate a run must reach: an open-loop
// run that keeps up still ends a little under the rate it sends at.
const rateTolerance = 0.95

// Target is the assumption of ADR 0001 that a run verifies.
type Target struct {
	// Rate is the Events/s the server must accept.
	Rate float64
	// P99 is the bound on the 99th percentile of the ingest latency.
	P99 time.Duration
}

// Summary is the result of a run.
type Summary struct {
	Accepted int
	Failed   int
	// Rate is the accepted Events/s.
	Rate float64
	P50  time.Duration
	P99  time.Duration
	Max  time.Duration
	// Pass reports whether the run meets the target with no failed Events.
	Pass bool
}

// Summarize returns the summary of a run that took elapsed, from the
// latencies of the accepted Events and the number of failed ones.
func Summarize(latencies []time.Duration, failed int, elapsed time.Duration, target Target) Summary {
	s := Summary{Accepted: len(latencies), Failed: failed}
	if len(latencies) == 0 || elapsed <= 0 {
		return s
	}

	sorted := slices.Sorted(slices.Values(latencies))
	s.Rate = float64(len(sorted)) / elapsed.Seconds()
	s.P50 = rankPercentile(sorted, 50)
	s.P99 = rankPercentile(sorted, 99)
	s.Max = sorted[len(sorted)-1]
	s.Pass = failed == 0 && s.Rate >= target.Rate*rateTolerance && s.P99 < target.P99

	return s
}

// rankPercentile returns the nearest-rank p-th percentile of sorted, which is not
// empty.
func rankPercentile(sorted []time.Duration, p int) time.Duration {
	rank := (len(sorted)*p + 99) / 100
	return sorted[max(rank, 1)-1]
}
