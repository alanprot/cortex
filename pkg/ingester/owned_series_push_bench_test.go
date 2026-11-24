package ingester

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
	"github.com/weaveworks/common/user"

	"github.com/cortexproject/cortex/pkg/cortexpb"
	"github.com/cortexproject/cortex/pkg/ring"
	"github.com/cortexproject/cortex/pkg/util"
	"github.com/cortexproject/cortex/pkg/util/services"
	"github.com/cortexproject/cortex/pkg/util/test"
)

const (
	pushBenchSeriesPerReq = 1000
	pushBenchParallelism  = 8
	// Owned ranges as on a 30-ingester zone with 512 tokens each.
	pushBenchOwnedRanges = 512
	pushBenchZoneSize    = 30
)

type pushBenchMode struct {
	name              string
	ringOwnership     bool
	ownedSeriesLimits bool
}

var pushBenchModes = []pushBenchMode{
	{name: "off"},
	{name: "ring-ownership", ringOwnership: true},
	{name: "owned-series", ringOwnership: true, ownedSeriesLimits: true},
}

func newPushBenchIngester(b *testing.B, m pushBenchMode) *Ingester {
	cfg := defaultIngesterTestConfig(b)
	cfg.LifecyclerConfig.JoinAfter = 0
	cfg.UseRingOwnershipForLimits = m.ringOwnership
	cfg.OwnedSeriesLimitEnabled = m.ownedSeriesLimits
	cfg.DistributorShardByAllLabels = true
	limits := defaultLimitsTestConfig()
	limits.MaxLocalSeriesPerUser = 0
	limits.MaxGlobalSeriesPerUser = 0
	limits.MaxLocalSeriesPerMetric = 0
	limits.MaxGlobalSeriesPerMetric = 0
	i, err := prepareIngesterWithBlocksStorageAndLimits(b, cfg, limits, nil, "", prometheus.NewRegistry())
	require.NoError(b, err)
	require.NoError(b, services.StartAndAwaitRunning(context.Background(), i))
	b.Cleanup(func() { _ = services.StopAndAwaitTerminated(context.Background(), i) })
	test.Poll(b, 100*time.Millisecond, ring.ACTIVE, func() any { return i.lifecycler.GetState() })
	if i.ownershipRefresher != nil {
		// Keep the refresher from replacing the ranges set below.
		i.ownershipRefresher.setOwnedRanges = nil
	}
	return i
}

// randomOwnedRanges returns ranges covering about 1/zoneSize of the ring.
func randomOwnedRanges(r *rand.Rand, n, zoneSize int) []ring.TokenRange {
	tokens := make([]uint32, 0, n*zoneSize)
	for range n * zoneSize {
		tokens = append(tokens, r.Uint32())
	}
	slices.Sort(tokens)
	tokens = slices.Compact(tokens)
	out := make([]ring.TokenRange, 0, n)
	for k := 0; k+1 < len(tokens) && len(out) < n; k += zoneSize {
		out = append(out, ring.TokenRange{Start: tokens[k], End: tokens[k+1]})
	}
	return out
}

func pushBenchRequest(prefix string, base, n int, ts int64) *cortexpb.WriteRequest {
	lbls := make([]labels.Labels, 0, n)
	samples := make([]cortexpb.Sample, 0, n)
	for k := range n {
		lbls = append(lbls, labels.FromStrings(
			labels.MetricName, "bench_metric",
			"job", "bench",
			"instance", fmt.Sprintf("%s-%d", prefix, (base+k)/100),
			"series", fmt.Sprintf("%d", base+k),
		))
		samples = append(samples, cortexpb.Sample{TimestampMs: ts, Value: 1})
	}
	return cortexpb.ToWriteRequest(lbls, samples, nil, nil, cortexpb.API)
}

// primeTenant creates the tenant's TSDB and gives the tracker realistic ranges.
func primeTenant(b *testing.B, i *Ingester, ctx context.Context, m pushBenchMode) {
	_, err := i.Push(ctx, pushBenchRequest("prime", 0, 1, util.TimeToMillis(time.Now())))
	require.NoError(b, err)
	if !m.ownedSeriesLimits {
		return
	}
	db, err := i.getTSDB(userID)
	require.NoError(b, err)
	require.NotNil(b, db.ownedSeries, "owned-series tracker not enabled")
	db.ownedSeries.setRanges(randomOwnedRanges(rand.New(rand.NewSource(1)), pushBenchOwnedRanges, pushBenchZoneSize))
}

// BenchmarkPushOwnedSeries measures Push with owned-series tracking off and on.
// One op is a Push of pushBenchSeriesPerReq samples.
func BenchmarkPushOwnedSeries(b *testing.B) {
	for _, m := range pushBenchModes {
		b.Run("new-series/"+m.name, func(b *testing.B) {
			i := newPushBenchIngester(b, m)
			ctx := user.InjectOrgID(context.Background(), userID)
			primeTenant(b, i, ctx, m)
			ts := util.TimeToMillis(time.Now())
			b.ReportAllocs()
			for n := 0; b.Loop(); n++ {
				_, err := i.Push(ctx, pushBenchRequest("s", n*pushBenchSeriesPerReq, pushBenchSeriesPerReq, ts))
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*pushBenchSeriesPerReq), "ns/series")
		})

		b.Run("new-series-parallel/"+m.name, func(b *testing.B) {
			i := newPushBenchIngester(b, m)
			ctx := user.InjectOrgID(context.Background(), userID)
			primeTenant(b, i, ctx, m)
			ts := util.TimeToMillis(time.Now())
			var next atomic.Int64
			b.SetParallelism(pushBenchParallelism)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					n := int(next.Add(1))
					if _, err := i.Push(ctx, pushBenchRequest("p", n*pushBenchSeriesPerReq, pushBenchSeriesPerReq, ts)); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})

		b.Run("append-existing/"+m.name, func(b *testing.B) {
			i := newPushBenchIngester(b, m)
			ctx := user.InjectOrgID(context.Background(), userID)
			primeTenant(b, i, ctx, m)
			ts := util.TimeToMillis(time.Now())
			const existing = 10 * pushBenchSeriesPerReq
			reqs := make([]*cortexpb.WriteRequest, 0, existing/pushBenchSeriesPerReq)
			for k := 0; k < existing; k += pushBenchSeriesPerReq {
				reqs = append(reqs, pushBenchRequest("a", k, pushBenchSeriesPerReq, ts))
				_, err := i.Push(ctx, reqs[len(reqs)-1])
				require.NoError(b, err)
			}
			b.ReportAllocs()
			for n := 0; b.Loop(); n++ {
				req := pushBenchRequest("a", (n%len(reqs))*pushBenchSeriesPerReq, pushBenchSeriesPerReq, ts+int64(n+1))
				if _, err := i.Push(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
