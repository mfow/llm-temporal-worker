package engine

import (
	"context"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/mfow/llm-temporal-worker/llm"
)

// BenchmarkGenerateMemoryAdmissionAndCompile measures the deterministic,
// in-process successful Generate path with the memory admission store. It is
// a local proxy for admission and compilation work: it deliberately has no
// Redis server or provider network request, so it does not prove either SLO.
func BenchmarkGenerateMemoryAdmissionAndCompile(b *testing.B) {
	adapter := &fakeAdapter{name: "benchmark", response: successfulResponse()}
	harness := newHarness(b, adapter)
	requests := make([]requestWithDuration, b.N)
	for index := range requests {
		requests[index].request = baseRequest("benchmark-memory-" + strconv.Itoa(index))
	}

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for index := range requests {
		started := time.Now()
		if _, err := harness.engine.Generate(ctx, requests[index].request); err != nil {
			b.Fatal(err)
		}
		requests[index].duration = time.Since(started)
	}
	b.StopTimer()
	b.ReportMetric(float64(p99Duration(requests))/float64(time.Millisecond), "p99_ms/op")
}

type requestWithDuration struct {
	request  llm.Request
	duration time.Duration
}

func p99Duration(samples []requestWithDuration) time.Duration {
	durations := make([]time.Duration, len(samples))
	for index, sample := range samples {
		durations[index] = sample.duration
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	if len(durations) == 0 {
		return 0
	}
	rank := (99*len(durations) + 99) / 100
	return durations[rank-1]
}
