package main

import (
	"context"
	"fmt"
	"runtime"
	"syscall"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/index"

	cortex_tsdb "github.com/cortexproject/cortex/pkg/storage/tsdb"
)

func cpuMs() int64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return ru.Utime.Sec*1000 + ru.Utime.Usec/1000 + ru.Stime.Sec*1000 + ru.Stime.Usec/1000
}

func main() {
	const iters = 200

	opts := tsdb.DefaultHeadOptions()
	opts.ChunkRange = 3600000
	h, _ := tsdb.NewHead(nil, nil, nil, nil, opts, nil)
	ctx := context.Background()
	app := h.Appender(ctx)
	for i := 0; i < 1_000_000; i++ {
		lset := labels.FromStrings("__name__", "cpu", "pod", fmt.Sprintf("my-svc-%06d-xk2rm", i%150000), "job", fmt.Sprintf("job-%03d", i%200), "instance", fmt.Sprintf("i%d", i))
		app.Append(0, lset, int64(i/50000)*15000, float64(i))
		if (i+1)%50000 == 0 {
			app.Commit()
			app = h.Appender(ctx)
		}
	}
	app.Commit()
	fmt.Println("Loaded 1M series (200 jobs × 5K each, 150K pods)")

	matchers := []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
		labels.MustNewMatcher(labels.MatchEqual, "job", "job-001"),
		labels.MustNewMatcher(labels.MatchRegexp, "pod", ".*123.*"),
	}
	fmt.Printf("Query: %v\n\n", matchers)

	// Baseline: no cache
	ir, _ := h.Index()
	runtime.GC()
	c0 := cpuMs()
	for i := 0; i < iters; i++ {
		p, _ := tsdb.PostingsForMatchers(ctx, ir, matchers...)
		index.ExpandPostings(p)
	}
	c1 := cpuMs()
	fmt.Printf("No cache (full scan x%d):            %dms  (%dms/query)\n", iters, c1-c0, (c1-c0)/int64(iters))
	ir.Close()

	// Incremental cache with continuous churn
	cache := cortex_tsdb.NewIncrementalHeadPostingsCacheForTest()
	ir, _ = h.Index()
	runtime.GC()

	c0 = cpuMs()
	for i := 0; i < iters; i++ {
		// Add 10 new series per query (simulating churn)
		app = h.Appender(ctx)
		for j := 0; j < 10; j++ {
			idx := 1_000_000 + i*10 + j
			lset := labels.FromStrings("__name__", "cpu", "pod", fmt.Sprintf("my-svc-%06d-xk2rm", idx%150000), "job", fmt.Sprintf("job-%03d", idx%200), "instance", fmt.Sprintf("i%d", idx))
			app.Append(0, lset, int64(idx)*15000, float64(idx))
		}
		app.Commit()
		ir.Close()
		ir, _ = h.Index()

		cache.PostingsForMatchers(ctx, ir, matchers...)
	}
	c1 = cpuMs()
	fmt.Printf("Incremental cache (churn x%d):       %dms  (%dms/query)\n", iters, c1-c0, (c1-c0)/int64(iters))

	ir.Close()
	h.Close()
}
