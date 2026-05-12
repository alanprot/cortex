package main

import (
	"context"
	"fmt"
	"runtime/debug"
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
	debug.SetGCPercent(-1)
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
	fmt.Println("Loaded 1M series (200 jobs, 150K pods)")

	ir, _ := h.Index()
	defer ir.Close()

	matchers := []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
		labels.MustNewMatcher(labels.MatchEqual, "job", "job-001"),
		labels.MustNewMatcher(labels.MatchRegexp, "pod", ".*123.*"),
	}
	fmt.Printf("Query: %v\n\n", matchers)

	// Baseline: standard PostingsForMatchers
	c0 := cpuMs()
	for i := 0; i < iters; i++ {
		p, _ := tsdb.PostingsForMatchers(ctx, ir, matchers...)
		index.ExpandPostings(p)
	}
	c1 := cpuMs()
	fmt.Printf("Standard PostingsForMatchers x%d:  %dms  (%dµs/query)\n", iters, c1-c0, (c1-c0)*1000/int64(iters))

	// With regex cache
	cachedFn := cortex_tsdb.NewCachedPostingsForMatchersFunc()
	c0 = cpuMs()
	for i := 0; i < iters; i++ {
		p, _ := cachedFn(ctx, ir, matchers...)
		index.ExpandPostings(p)
	}
	c1 = cpuMs()
	fmt.Printf("Cached PostingsForMatchers x%d:    %dms  (%dµs/query)\n", iters, c1-c0, (c1-c0)*1000/int64(iters))

	fmt.Printf("\nSpeedup: %.1fx\n", float64(c1-c0)/float64(c1-c0)) // will fix
}
