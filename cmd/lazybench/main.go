package main

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/index"

	cortex_tsdb "github.com/cortexproject/cortex/pkg/storage/tsdb"
)

func cpuMillis() int64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return ru.Utime.Sec*1000 + ru.Utime.Usec/1000 + ru.Stime.Sec*1000 + ru.Stime.Usec/1000
}

func main() {
	const (
		numSeries     = 1_000_000
		numPods       = 150_000
		numServices   = 5
		numNamespaces = 3
		batchSize     = 50_000
		queryIters    = 100
	)

	services := []string{"api", "worker", "gateway", "frontend", "backend"}
	namespaces := []string{"prod", "staging", "dev"}

	fmt.Println("=== Lazy Matcher Benchmark ===")
	fmt.Printf("Series: %d, Pods: %d\n", numSeries, numPods)

	// Create head once
	opts := tsdb.DefaultHeadOptions()
	opts.ChunkRange = 3600000
	h, _ := tsdb.NewHead(nil, nil, nil, nil, opts, nil)

	fmt.Print("Loading series...")
	ctx := context.Background()
	app := h.Appender(ctx)
	for i := 0; i < numSeries; i++ {
		lset := labels.FromStrings(
			"__name__", "http_requests_total",
			"pod", fmt.Sprintf("pod-%06d", i%numPods),
			"service", services[i%numServices],
			"namespace", namespaces[i%numNamespaces],
			"instance", fmt.Sprintf("inst-%d", i),
		)
		app.Append(0, lset, int64(i/batchSize)*15000, float64(i))
		if (i+1)%batchSize == 0 {
			app.Commit()
			app = h.Appender(ctx)
		}
	}
	app.Commit()
	fmt.Println(" done")

	ir, _ := h.Index()
	defer ir.Close()
	defer h.Close()

	headULID := cortex_tsdb.HeadULIDForTest()

	// Test queries with different selectivity
	queries := []struct {
		name     string
		matchers []*labels.Matcher
	}{
		{
			"service=api (200K series) + pod regex",
			[]*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__name__", "http_requests_total"),
				labels.MustNewMatcher(labels.MatchEqual, "service", "api"),
				labels.MustNewMatcher(labels.MatchRegexp, "pod", ".*123.*"),
			},
		},
		{
			"service=api+ns=prod (66K series) + pod regex",
			[]*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__name__", "http_requests_total"),
				labels.MustNewMatcher(labels.MatchEqual, "service", "api"),
				labels.MustNewMatcher(labels.MatchEqual, "namespace", "prod"),
				labels.MustNewMatcher(labels.MatchRegexp, "pod", ".*123.*"),
			},
		},
		{
			"__name__ only (1M series) + pod regex",
			[]*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__name__", "http_requests_total"),
				labels.MustNewMatcher(labels.MatchRegexp, "pod", ".*123.*"),
			},
		},
	}

	fmt.Printf("\n%-50s %10s %10s %10s\n", "Query", "Disabled", "Enabled", "Speedup")
	fmt.Println("--------------------------------------------------------------------------------------------")

	for _, q := range queries {
		var cpuDisabled, cpuEnabled int64

		for _, lazy := range []int{0, 10000} {
			cfg := cortex_tsdb.TSDBPostingsCacheConfig{
				Head: cortex_tsdb.PostingsCacheConfig{
					Enabled:  true,
					MaxBytes: 100 * 1024 * 1024,
					Ttl:      10 * time.Minute,
				},
				PostingsForMatchers:       tsdb.PostingsForMatchers,
				LazyMatcherMaxCardinality: lazy,
			}
			metrics := cortex_tsdb.NewPostingCacheMetrics(prometheus.NewRegistry())
			cache := cortex_tsdb.NewExpandedPostingsCacheForTest(cfg, metrics)

			cpuBefore := cpuMillis()
			for i := 0; i < queryIters; i++ {
				cache.ExpireSeries(labels.FromStrings("__name__", "http_requests_total"))
				p, _ := cache.PostingsForMatchers(ctx, headULID, ir, q.matchers...)
				refs, _ := index.ExpandPostings(p)
				_ = refs
			}
			cpuAfter := cpuMillis()

			if lazy == 0 {
				cpuDisabled = cpuAfter - cpuBefore
			} else {
				cpuEnabled = cpuAfter - cpuBefore
			}
		}

		speedup := float64(cpuDisabled) / float64(cpuEnabled)
		fmt.Printf("%-50s %8dms %8dms %9.2fx\n", q.name, cpuDisabled, cpuEnabled, speedup)
	}
}
