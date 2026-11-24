package ingester

import (
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cortexproject/cortex/pkg/ingester/client"
	"github.com/cortexproject/cortex/pkg/ring"
	"github.com/cortexproject/cortex/pkg/ring/kv/consul"
	"github.com/cortexproject/cortex/pkg/util"
	"github.com/cortexproject/cortex/pkg/util/validation"
)

const (
	ownedTestSeries      = 1000
	ownedTestSmallShard  = 3
	ownedTestLargeShard  = 6
	ownedTestZones       = 3
	ownedTestMaxTenants  = 1000
	ownedTestRaceWorkers = 8
	ownedTestRaceOps     = 2000
)

var ownedTestLayout = ownershipTestRingLayout{zones: ownedTestZones, perZone: 6, tokensPerInstance: 16}

func inRanges(ranges []ring.TokenRange, token uint32) bool {
	for _, r := range ranges {
		if (r.Start < r.End && token >= r.Start && token < r.End) || (r.Start >= r.End && (token >= r.Start || token < r.End)) {
			return true
		}
	}
	return false
}

func ownedTestSeriesLabels(i int) labels.Labels {
	return labels.FromStrings(labels.MetricName, "metric", "series", fmt.Sprint(i))
}

// newOwnedSeriesTestIngester returns an ingester tracking owned series whose
// refresher reads the ring from *desc.
func newOwnedSeriesTestIngester(t *testing.T, dataDir string, tenantLimits *staticTenantLimits, desc **ring.Desc) *Ingester {
	t.Helper()
	cfg := defaultIngesterTestConfig(t)
	cfg.LifecyclerConfig.ID = ownershipTestInstanceID(0, 0)
	cfg.LifecyclerConfig.Zone = "zone-0"
	cfg.LifecyclerConfig.RingConfig.ZoneAwarenessEnabled = true
	cfg.LifecyclerConfig.RingConfig.ReplicationFactor = ownedTestZones
	cfg.UseRingOwnershipForLimits = true
	cfg.OwnedSeriesLimitEnabled = true
	cfg.DistributorShardByAllLabels = true
	cfg.DistributorShardingStrategy = util.ShardingStrategyShuffle
	limits := defaultLimitsTestConfig()
	limits.MaxLocalSeriesPerUser = 0
	i, err := prepareIngesterWithBlocksStorageAndLimits(t, cfg, limits, tenantLimits, dataDir, prometheus.NewRegistry())
	require.NoError(t, err)
	i.ownershipRefresher.latest = func() *ring.Desc { return *desc }
	return i
}

func appendOwnedTestSeries(db *userTSDB, lbls labels.Labels) error {
	app := db.Appender(context.Background())
	if _, err := app.Append(0, lbls, time.Now().UnixMilli(), 1); err != nil {
		_ = app.Rollback()
		return err
	}
	return app.Commit()
}

// pickOwnedTestTenant returns a tenant whose small and large shards include me.
func pickOwnedTestTenant(t *testing.T, r *ring.Ring, me string) string {
	for n := range ownedTestMaxTenants {
		tenant := fmt.Sprintf("tenant-%d", n)
		if r.ShuffleShard(tenant, ownedTestSmallShard).HasInstance(me) && r.ShuffleShard(tenant, ownedTestLargeShard).HasInstance(me) {
			return tenant
		}
	}
	t.Fatal("no tenant found")
	return ""
}

// A larger shard moves series away: they stop counting, so the limit does not
// reject, and count again once the shard shrinks back.
func TestOwnedSeries_ShardSizeChange(t *testing.T) {
	desc := newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), ownedTestLayout)
	me := ownershipTestInstanceID(0, 0)
	r := newOwnershipTestRing(t, ownedTestZones, nil)
	r.UpdateDesc(desc)
	tenant := pickOwnedTestTenant(t, r, me)

	large := r.ShuffleShard(tenant, ownedTestLargeShard).(ring.ZonalOwnershipRing)
	largeRanges := large.ZonalOwnedRanges(me)
	largeShare := large.ZonalOwnership()[me]
	require.Less(t, largeShare, 1.0)
	expectedOwned := 0
	for n := range ownedTestSeries {
		if inRanges(largeRanges, client.ShardByAllLabels(tenant, ownedTestSeriesLabels(n))) {
			expectedOwned++
		}
	}
	require.Less(t, expectedOwned, ownedTestSeries)

	// The local limit with the large shard falls between owned and head series.
	globalLimit := int(float64(expectedOwned+ownedTestSeries) / 2 / largeShare)
	require.GreaterOrEqual(t, globalLimit, ownedTestSeries)
	tenantLimit := &validation.Limits{IngestionTenantShardSize: ownedTestSmallShard, MaxGlobalSeriesPerUser: globalLimit}
	tenantLimits := &staticTenantLimits{limits: map[string]*validation.Limits{tenant: tenantLimit}}
	i := newOwnedSeriesTestIngester(t, "", tenantLimits, &desc)
	t.Cleanup(i.closeAllTSDB)
	db, err := i.getOrCreateTSDB(tenant, true)
	require.NoError(t, err)

	i.ownershipRefresher.refresh(context.Background(), []string{tenant})
	for n := range ownedTestSeries {
		require.NoError(t, appendOwnedTestSeries(db, ownedTestSeriesLabels(n)))
	}
	assert.Equal(t, int64(ownedTestSeries), db.ownedSeries.owned())

	tenantLimit.IngestionTenantShardSize = ownedTestLargeShard
	i.ownershipRefresher.refresh(context.Background(), []string{tenant})
	assert.Equal(t, int64(expectedOwned), db.ownedSeries.owned())
	assert.Equal(t, float64(expectedOwned), testutil.ToFloat64(i.ownershipRefresher.ownedSeriesGauge.WithLabelValues(tenant)))
	require.Error(t, i.limiter.AssertMaxSeriesPerUser(tenant, ownedTestSeries), "the head count would be rejected")
	require.NoError(t, appendOwnedTestSeries(db, ownedTestSeriesLabels(ownedTestSeries)))

	tenantLimit.IngestionTenantShardSize = ownedTestSmallShard
	i.ownershipRefresher.refresh(context.Background(), []string{tenant})
	assert.Equal(t, int64(ownedTestSeries+1), db.ownedSeries.owned())
	assert.Zero(t, db.ownedSeries.notOwned.Load())

	db.PostDeletion(map[chunks.HeadSeriesRef]labels.Labels{1: ownedTestSeriesLabels(0)})
	assert.Equal(t, int64(ownedTestSeries), db.ownedSeries.owned())
}

func TestOwnedSeries_WALReplayRestoresTracker(t *testing.T) {
	desc := newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), ownedTestLayout)
	const tenant = "tenant"
	dataDir := t.TempDir()
	tenantLimits := &staticTenantLimits{limits: map[string]*validation.Limits{}}

	i := newOwnedSeriesTestIngester(t, dataDir, tenantLimits, &desc)
	db, err := i.getOrCreateTSDB(tenant, true)
	require.NoError(t, err)
	for n := range ownedTestSeries {
		require.NoError(t, appendOwnedTestSeries(db, ownedTestSeriesLabels(n)))
	}
	i.closeAllTSDB()

	i = newOwnedSeriesTestIngester(t, dataDir, tenantLimits, &desc)
	t.Cleanup(i.closeAllTSDB)
	db, err = i.getOrCreateTSDB(tenant, true)
	require.NoError(t, err)
	require.Equal(t, uint64(ownedTestSeries), db.Head().NumSeries())
	assert.Equal(t, int64(ownedTestSeries), db.ownedSeries.total)
	for n := range ownedTestSeries {
		assert.True(t, db.ownedSeries.tokens.Contains(client.ShardByAllLabels(tenant, ownedTestSeriesLabels(n))))
	}
}

func TestOwnedSeriesTracker_TokenCollision(t *testing.T) {
	const (
		token = uint32(100)
		other = uint32(500)
	)
	owned := []ring.TokenRange{{Start: 0, End: other}}
	tr := newOwnedSeriesTracker()
	tr.setRanges(owned)
	tr.add(token)
	tr.add(token)
	tr.add(other)
	assert.Equal(t, int64(2), tr.owned())
	assert.Equal(t, int64(1), tr.notOwned.Load())

	// Recount includes the extra series of the shared token.
	tr.setRanges([]ring.TokenRange{{Start: token, End: token + 1}})
	assert.Equal(t, int64(2), tr.owned())
	tr.setRanges(owned)

	tr.remove(token)
	assert.Equal(t, int64(1), tr.owned())
	assert.True(t, tr.tokens.Contains(token), "one series still has the token")
	tr.remove(token)
	assert.Equal(t, int64(0), tr.owned())
	assert.False(t, tr.tokens.Contains(token))
	tr.remove(token) // Untracked: no-op.
	tr.remove(other)
	assert.Equal(t, int64(0), tr.total)
	assert.Zero(t, tr.notOwned.Load())
}

func TestOwnedSeriesTracker_RecountMatchesBruteForce(t *testing.T) {
	const (
		tokens = 5000
		rounds = 50
		maxRng = 8
	)
	rnd := rand.New(rand.NewSource(ownershipTestSeed))
	tr := newOwnedSeriesTracker()
	all := make([]uint32, 0, tokens)
	for range tokens {
		tok := rnd.Uint32()
		all = append(all, tok)
		tr.add(tok)
	}
	for round := range rounds {
		ranges := make([]ring.TokenRange, 1+rnd.Intn(maxRng))
		for k := range ranges {
			ranges[k] = ring.TokenRange{Start: rnd.Uint32(), End: rnd.Uint32()}
		}
		if round == 0 {
			ranges = []ring.TokenRange{{Start: 7, End: 7}} // Whole ring.
		}
		tr.setRanges(ranges)
		want := 0
		for _, tok := range all {
			if inRanges(ranges, tok) {
				want++
			}
		}
		require.Equal(t, int64(want), tr.owned(), "round %d", round)
	}
	tr.clearRanges()
	assert.Equal(t, int64(tokens), tr.owned())
}

// Hooks running during recounts keep notOwned consistent with the final state.
func TestOwnedSeriesTracker_ConcurrentHooksAndRecount(t *testing.T) {
	rangeSets := [][]ring.TokenRange{
		{{Start: 0, End: 1 << 31}},
		{{Start: 3 << 30, End: 1 << 30}},
		{{Start: 1 << 29, End: 1 << 30}, {Start: 1 << 31, End: 3 << 30}},
	}
	tr := newOwnedSeriesTracker()
	var (
		wg       sync.WaitGroup
		mtx      sync.Mutex
		live     []uint32
		stopped  = make(chan struct{})
		recounts sync.WaitGroup
	)
	recounts.Add(1)
	go func() {
		defer recounts.Done()
		for n := 0; ; n++ {
			select {
			case <-stopped:
				return
			default:
				tr.setRanges(rangeSets[n%len(rangeSets)])
			}
		}
	}()
	for w := range ownedTestRaceWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rnd := rand.New(rand.NewSource(int64(w)))
			var mine []uint32
			for range ownedTestRaceOps {
				tok := rnd.Uint32() % (1 << 12) // Small space forces collisions.
				tok <<= 20
				tr.add(tok)
				mine = append(mine, tok)
				if rnd.Intn(2) == 0 {
					k := rnd.Intn(len(mine))
					tr.remove(mine[k])
					mine = append(mine[:k], mine[k+1:]...)
				}
			}
			mtx.Lock()
			live = append(live, mine...)
			mtx.Unlock()
		}()
	}
	wg.Wait()
	close(stopped)
	recounts.Wait()

	final := tr.intervals
	want := 0
	for _, tok := range live {
		for _, iv := range final {
			if uint64(tok) >= iv.start && uint64(tok) < iv.end {
				want++
				break
			}
		}
	}
	assert.Equal(t, int64(len(live)), tr.total)
	assert.Equal(t, int64(want), tr.owned())
}

const (
	ownedBenchSeries  = 1_000_000
	ownedBenchTenant  = "tenant"
	ownedBenchPerZone = 30
	ownedBenchTokens  = 128
)

func BenchmarkOwnedSeries(b *testing.B) {
	lbls := make([]labels.Labels, ownedBenchSeries)
	for n := range lbls {
		lbls[n] = labels.FromStrings(labels.MetricName, "metric", "pod", fmt.Sprintf("pod-%d", n%1000), "series", fmt.Sprint(n))
	}
	tokens := make([]uint32, ownedBenchSeries)
	for n, l := range lbls {
		tokens[n] = client.ShardByAllLabels(ownedBenchTenant, l)
	}

	b.Run("token", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			tokens[n%ownedBenchSeries] = client.ShardByAllLabels(ownedBenchTenant, lbls[n%ownedBenchSeries])
		}
	})

	// Hashing plus tracking, with the ranges of a 1/30 share.
	b.Run("hook", func(b *testing.B) {
		var tr *ownedSeriesTracker
		for n := 0; n < b.N; n++ {
			if n%ownedBenchSeries == 0 {
				b.StopTimer()
				tr = newOwnedSeriesTracker()
				tr.setRanges(ownedBenchRanges(b)[0])
				b.StartTimer()
			}
			tr.add(client.ShardByAllLabels(ownedBenchTenant, lbls[n%ownedBenchSeries]))
		}
	})

	// Head churn at 1M series: one series deleted and one created per op.
	b.Run("hook-steady", func(b *testing.B) {
		tr := newOwnedSeriesTracker()
		tr.setRanges(ownedBenchRanges(b)[0])
		for _, tok := range tokens {
			tr.add(tok)
		}
		b.ResetTimer()
		for n := 0; n < b.N; n++ {
			k := n % ownedBenchSeries
			tr.remove(client.ShardByAllLabels(ownedBenchTenant, lbls[k]))
			tr.add(client.ShardByAllLabels(ownedBenchTenant, lbls[k]))
		}
	})

	b.Run("memory", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			tr := newOwnedSeriesTracker()
			for _, tok := range tokens {
				tr.add(tok)
			}
			runtime.GC()
			runtime.ReadMemStats(&after)
			b.ReportMetric(float64(tr.tokens.GetSizeInBytes()), "bitmap-bytes")
			b.ReportMetric(float64(after.HeapAlloc)-float64(before.HeapAlloc), "heap-delta-bytes")
			b.ReportMetric(float64(len(tr.extra)), "collided-tokens")
			b.ReportMetric(float64(tr.tokens.Stats().Containers), "containers")
			runtime.KeepAlive(tr)
		}
	})

	b.Run("recount", func(b *testing.B) {
		rangeSets := ownedBenchRanges(b)
		tr := newOwnedSeriesTracker()
		for _, tok := range tokens {
			tr.add(tok)
		}
		b.ResetTimer()
		for n := 0; n < b.N; n++ {
			tr.setRanges(rangeSets[n%len(rangeSets)])
		}
		b.ReportMetric(float64(len(rangeSets[0])), "ranges-whole-ring")
	})
}

// ownedBenchRanges returns an instance's ranges in a 90-instance ring, then in
// shards of growing size.
func ownedBenchRanges(b *testing.B) [][]ring.TokenRange {
	desc := newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), ownershipTestRingLayout{zones: ownedTestZones, perZone: ownedBenchPerZone, tokensPerInstance: ownedBenchTokens})
	kvClient, closer := consul.NewInMemoryClient(ring.GetCodec(), log.NewNopLogger(), nil)
	b.Cleanup(func() { _ = closer.Close() })
	r, err := ring.NewDescFedRing(ring.Config{HeartbeatTimeout: time.Minute, ReplicationFactor: ownedTestZones, ZoneAwarenessEnabled: true}, "bench", ownershipTestRingKey, kvClient, log.NewNopLogger())
	require.NoError(b, err)
	r.UpdateDesc(desc)
	me := ownershipTestInstanceID(0, 0)
	rangeSets := [][]ring.TokenRange{r.ZonalOwnedRanges(me)}
	for _, size := range []int{ownedTestLargeShard, 2 * ownedTestLargeShard, 4 * ownedTestLargeShard} {
		for n := 0; ; n++ {
			if sub := r.ShuffleShard(fmt.Sprintf("t-%d", n), size); sub.HasInstance(me) {
				rangeSets = append(rangeSets, sub.(ring.ZonalOwnershipRing).ZonalOwnedRanges(me))
				break
			}
		}
	}
	return rangeSets
}
