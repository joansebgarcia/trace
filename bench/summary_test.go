package main

import (
	"testing"
	"time"
)

var target = Target{Rate: 500, P99: 50 * time.Millisecond}

func TestSummarize(t *testing.T) {
	cases := []struct {
		name      string
		latencies []time.Duration
		failed    int
		elapsed   time.Duration
		want      Summary
	}{
		{
			name:      "one slow Event in a hundred keeps p99 under the target",
			latencies: append(repeat(99, 10*time.Millisecond), 80*time.Millisecond),
			elapsed:   200 * time.Millisecond,
			want:      Summary{Accepted: 100, Rate: 500, P50: 10 * time.Millisecond, P99: 10 * time.Millisecond, Max: 80 * time.Millisecond, Pass: true},
		},
		{
			name:      "p99 over the target fails",
			latencies: ramp(100),
			elapsed:   200 * time.Millisecond,
			want:      Summary{Accepted: 100, Rate: 500, P50: 50 * time.Millisecond, P99: 99 * time.Millisecond, Max: 100 * time.Millisecond},
		},
		{
			name:      "rate under the target fails",
			latencies: repeat(100, 10*time.Millisecond),
			elapsed:   time.Second,
			want:      Summary{Accepted: 100, Rate: 100, P50: 10 * time.Millisecond, P99: 10 * time.Millisecond, Max: 10 * time.Millisecond},
		},
		{
			name:      "rejected Events fail",
			latencies: repeat(100, 10*time.Millisecond),
			failed:    1,
			elapsed:   200 * time.Millisecond,
			want:      Summary{Accepted: 100, Failed: 1, Rate: 500, P50: 10 * time.Millisecond, P99: 10 * time.Millisecond, Max: 10 * time.Millisecond},
		},
		{
			name:    "no accepted Events fails",
			elapsed: time.Second,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Summarize(c.latencies, c.failed, c.elapsed, target)
			if got != c.want {
				t.Errorf("summary = %+v, want %+v", got, c.want)
			}
		})
	}
}

// repeat returns n latencies of d.
func repeat(n int, d time.Duration) []time.Duration {
	latencies := make([]time.Duration, n)
	for i := range latencies {
		latencies[i] = d
	}
	return latencies
}

// ramp returns the latencies 1 ms to n ms, last first.
func ramp(n int) []time.Duration {
	latencies := make([]time.Duration, n)
	for i := range latencies {
		latencies[i] = time.Duration(n-i) * time.Millisecond
	}
	return latencies
}
