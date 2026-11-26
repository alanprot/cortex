package ingester

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cortexproject/cortex/pkg/ingester/client"
	"github.com/cortexproject/cortex/pkg/ring"
	"github.com/cortexproject/cortex/pkg/util"
	"github.com/cortexproject/cortex/pkg/util/validation"
)

// newOwnedScenarioIngester is newOwnedSeriesTestIngester for any instance in
// zone-0, with an optional config change.
func newOwnedScenarioIngester(t *testing.T, me, dataDir string, tenantLimits *staticTenantLimits, desc **ring.Desc, mod func(*Config)) *Ingester {
	t.Helper()
	cfg := defaultIngesterTestConfig(t)
	cfg.LifecyclerConfig.ID = me
	cfg.LifecyclerConfig.Zone = "zone-0"
	cfg.LifecyclerConfig.RingConfig.ZoneAwarenessEnabled = true
	cfg.LifecyclerConfig.RingConfig.ReplicationFactor = ownedTestZones
	cfg.UseRingOwnershipForLimits = true
	cfg.OwnedSeriesLimitEnabled = true
	cfg.DistributorShardByAllLabels = true
	cfg.DistributorShardingStrategy = util.ShardingStrategyShuffle
	if mod != nil {
		mod(&cfg)
	}
	limits := defaultLimitsTestConfig()
	limits.MaxLocalSeriesPerUser = 0
	i, err := prepareIngesterWithBlocksStorageAndLimits(t, cfg, limits, tenantLimits, dataDir, prometheus.NewRegistry())
	require.NoError(t, err)
	if i.ownershipRefresher != nil {
		i.ownershipRefresher.latest = func() *ring.Desc { return *desc }
	}
	return i
}

// ownedScenarioView returns the ring, or the tenant's subring, that the
// distributor routes the tenant's writes within.
func ownedScenarioView(t *testing.T, desc *ring.Desc, tenant string, shardSize int) ring.ZonalOwnershipRing {
	t.Helper()
	r := newOwnershipTestRing(t, ownedTestZones, nil)
	r.UpdateDesc(cloneOwnershipTestDesc(desc))
	if shardSize > 0 {
		return r.ShuffleShard(tenant, shardSize).(ring.ZonalOwnershipRing)
	}
	return r
}

// ownedScenarioSeries returns n new series whose tokens keep accepts, numbered
// from *next on.
func ownedScenarioSeries(tenant string, n int, next *int, keep func(uint32) bool) []labels.Labels {
	out := make([]labels.Labels, 0, n)
	for len(out) < n {
		l := labels.FromStrings(labels.MetricName, "metric", "series", fmt.Sprint(*next))
		*next++
		if keep == nil || keep(client.ShardByAllLabels(tenant, l)) {
			out = append(out, l)
		}
	}
	return out
}

func countInRanges(tenant string, series []labels.Labels, ranges []ring.TokenRange) int {
	n := 0
	for _, l := range series {
		if inRanges(ranges, client.ShardByAllLabels(tenant, l)) {
			n++
		}
	}
	return n
}

// withoutLastPerZone returns desc without the last instance of each zone: the
// ring before a scale-up that added them.
func withoutLastPerZone(desc *ring.Desc, perZone int) *ring.Desc {
	out := cloneOwnershipTestDesc(desc)
	for id := range out.Ingesters {
		if strings.HasSuffix(id, fmt.Sprintf("-ingester-%d", perZone-1)) {
			delete(out.Ingesters, id)
		}
	}
	return out
}

// pickShrinkingInstance returns the zone-0 instance, present before and after,
// whose share shrinks the most with the scale-up.
func pickShrinkingInstance(t *testing.T, before, after *ring.Desc) string {
	t.Helper()
	b := ownedScenarioView(t, before, "", 0).ZonalOwnership()
	a := ownedScenarioView(t, after, "", 0).ZonalOwnership()
	best, bestRatio := "", 1.0
	for id := range before.Ingesters {
		if !strings.HasPrefix(id, "zone-0-") || b[id] == 0 {
			continue
		}
		if r := a[id] / b[id]; r < bestRatio {
			best, bestRatio = id, r
		}
	}
	require.Less(t, bestRatio, 0.9, "no zone-0 instance lost enough of its range")
	return best
}

// scaleUpScenario is an instance holding series routed to it before a scale-up.
type scaleUpScenario struct {
	tenant          string
	me              string
	before, after   *ring.Desc
	held            []labels.Labels
	afterRanges     []ring.TokenRange
	stillOwned      int
	globalLimit     int
	nextSeriesIndex int
}

func newScaleUpScenario(t *testing.T) *scaleUpScenario {
	t.Helper()
	s := &scaleUpScenario{tenant: "tenant"}
	s.after = newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), ownedTestLayout)
	s.before = withoutLastPerZone(s.after, ownedTestLayout.perZone)
	s.me = pickShrinkingInstance(t, s.before, s.after)

	beforeRanges := ownedScenarioView(t, s.before, s.tenant, 0).ZonalOwnedRanges(s.me)
	afterView := ownedScenarioView(t, s.after, s.tenant, 0)
	s.afterRanges = afterView.ZonalOwnedRanges(s.me)
	s.held = ownedScenarioSeries(s.tenant, ownedTestSeries, &s.nextSeriesIndex, func(tok uint32) bool { return inRanges(beforeRanges, tok) })
	s.stillOwned = countInRanges(s.tenant, s.held, s.afterRanges)
	require.Less(t, s.stillOwned, len(s.held)*9/10)

	// After the scale-up, the local limit falls between owned and in-memory series.
	s.globalLimit = int(float64(s.stillOwned+len(s.held)) / 2 / afterView.ZonalOwnership()[s.me])
	return s
}

// An old ingester keeps every series it held before a scale-up until head
// compaction. They must not count against the tenant's smaller local limit,
// which still applies to the series it owns.
func TestOwnedSeries_ScaleUpDoesNotThrottleLeftovers(t *testing.T) {
	ctx := context.Background()
	s := newScaleUpScenario(t)
	tenantLimit := &validation.Limits{}
	tenantLimits := &staticTenantLimits{limits: map[string]*validation.Limits{s.tenant: tenantLimit}}
	desc := cloneOwnershipTestDesc(s.before)
	i := newOwnedScenarioIngester(t, s.me, "", tenantLimits, &desc, nil)
	t.Cleanup(i.closeAllTSDB)
	db, err := i.getOrCreateTSDB(s.tenant, true)
	require.NoError(t, err)

	i.ownershipRefresher.refresh(ctx, []string{s.tenant})
	for _, l := range s.held {
		require.NoError(t, appendOwnedTestSeries(db, l))
	}
	require.Equal(t, int64(len(s.held)), db.ownedSeries.owned())

	desc = cloneOwnershipTestDesc(s.after)
	tenantLimit.MaxGlobalSeriesPerUser = s.globalLimit
	i.ownershipRefresher.refresh(ctx, []string{s.tenant})
	assert.Equal(t, int64(s.stillOwned), db.ownedSeries.owned())
	assert.Equal(t, uint64(len(s.held)), db.Head().NumSeries(), "the leftovers stay in memory")

	localLimit := i.limiter.maxSeriesPerUser(s.tenant)
	require.Greater(t, localLimit, s.stillOwned)
	require.Less(t, localLimit, len(s.held))
	require.Error(t, i.limiter.AssertMaxSeriesPerUser(s.tenant, len(s.held)), "counting every series in memory would throttle")

	// New series in the ranges it still owns are accepted up to the limit...
	fresh := ownedScenarioSeries(s.tenant, localLimit-s.stillOwned+1, &s.nextSeriesIndex, func(tok uint32) bool { return inRanges(s.afterRanges, tok) })
	for _, l := range fresh[:len(fresh)-1] {
		require.NoError(t, appendOwnedTestSeries(db, l))
	}
	assert.Equal(t, int64(localLimit), db.ownedSeries.owned())
	// ...and the limit still applies to the owned series.
	require.Error(t, appendOwnedTestSeries(db, fresh[len(fresh)-1]))
}

// After a restart, series replayed from the WAL count as owned until the first
// refresh, which then drops the leftovers from the count.
func TestOwnedSeries_RestartWithLeftoversRecountsOnFirstRefresh(t *testing.T) {
	ctx := context.Background()
	s := newScaleUpScenario(t)
	dataDir := t.TempDir()
	tenantLimit := &validation.Limits{}
	tenantLimits := &staticTenantLimits{limits: map[string]*validation.Limits{s.tenant: tenantLimit}}

	desc := cloneOwnershipTestDesc(s.before)
	i := newOwnedScenarioIngester(t, s.me, dataDir, tenantLimits, &desc, nil)
	db, err := i.getOrCreateTSDB(s.tenant, true)
	require.NoError(t, err)
	i.ownershipRefresher.refresh(ctx, []string{s.tenant})
	for _, l := range s.held {
		require.NoError(t, appendOwnedTestSeries(db, l))
	}
	i.closeAllTSDB()

	// Restart into the ring after the scale-up.
	desc = cloneOwnershipTestDesc(s.after)
	tenantLimit.MaxGlobalSeriesPerUser = s.globalLimit
	i = newOwnedScenarioIngester(t, s.me, dataDir, tenantLimits, &desc, nil)
	t.Cleanup(i.closeAllTSDB)
	db, err = i.getOrCreateTSDB(s.tenant, true)
	require.NoError(t, err)
	require.Equal(t, uint64(len(s.held)), db.Head().NumSeries())
	assert.Equal(t, int64(len(s.held)), db.ownedSeries.owned(), "before the first refresh every replayed series counts")

	i.ownershipRefresher.refresh(ctx, []string{s.tenant})
	assert.Equal(t, int64(s.stillOwned), db.ownedSeries.owned())
	assert.Equal(t, float64(s.stillOwned), testutil.ToFloat64(i.ownershipRefresher.ownedSeriesGauge.WithLabelValues(s.tenant)))
	require.Error(t, i.limiter.AssertMaxSeriesPerUser(s.tenant, len(s.held)), "counting every series in memory would throttle")
	fresh := ownedScenarioSeries(s.tenant, 1, &s.nextSeriesIndex, func(tok uint32) bool { return inRanges(s.afterRanges, tok) })
	require.NoError(t, appendOwnedTestSeries(db, fresh[0]))
}

// Head compaction removes series through PostDeletion: the tracker must hold
// exactly the series still in the head afterwards.
func TestOwnedSeries_FollowsHeadThroughCompaction(t *testing.T) {
	const (
		tenant     = "tenant"
		blockRange = 2 * time.Hour
	)
	me := ownershipTestInstanceID(0, 0)
	desc := newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), ownedTestLayout)
	tenantLimits := &staticTenantLimits{limits: map[string]*validation.Limits{tenant: {}}}
	i := newOwnedScenarioIngester(t, me, "", tenantLimits, &desc, func(cfg *Config) {
		cfg.BlocksStorageConfig.TSDB.BlockRanges = []time.Duration{blockRange}
	})
	t.Cleanup(i.closeAllTSDB)
	db, err := i.getOrCreateTSDB(tenant, true)
	require.NoError(t, err)
	i.ownershipRefresher.refresh(context.Background(), []string{tenant})

	push := func(name string, at ...time.Duration) {
		for _, ts := range at {
			app := db.Appender(context.Background())
			_, err := app.Append(0, labels.FromStrings(labels.MetricName, name), ts.Milliseconds(), 1)
			require.NoError(t, err)
			require.NoError(t, app.Commit())
		}
	}
	// The head spans [0, 3h30m], so compaction cuts [0, 2h) and truncates at 2h.
	push("live", 0)
	push("idle_long_ago", 0)
	push("idle_just_before_truncation", 0)
	push("live", time.Hour)
	push("idle_long_ago", time.Hour)
	push("idle_just_before_truncation", 2*time.Hour-5*time.Minute)
	push("live", 2*time.Hour, 3*time.Hour, 3*time.Hour+30*time.Minute)
	require.Equal(t, uint64(3), db.Head().NumSeries())
	require.Equal(t, int64(3), db.ownedSeries.total())

	i.compactBlocks(context.Background(), false, nil)

	require.Equal(t, uint64(1), db.Head().NumSeries(), "compaction should leave only the live series")
	assert.Equal(t, int64(1), db.ownedSeries.total(), "the tracker must hold exactly the series still in the head")
	ranges := ownedScenarioView(t, desc, tenant, 0).ZonalOwnedRanges(me)
	assert.Equal(t, int64(countInRanges(tenant, []labels.Labels{labels.FromStrings(labels.MetricName, "live")}, ranges)), db.ownedSeries.owned())

	push("live", 4*time.Hour)
	assert.Equal(t, int64(1), db.ownedSeries.total(), "a surviving series stays tracked once")
}

// A tenant moving between a shuffle-shard subring and the whole ring is
// recounted against the ring its writes are routed within each time.
func TestOwnedSeries_TenantFlipsBetweenShardedAndWholeRing(t *testing.T) {
	me := ownershipTestInstanceID(0, 0)
	desc := newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), ownedTestLayout)
	tenant := pickOwnedTestTenant(t, ownedScenarioView(t, desc, "", 0).(*ring.Ring), me)
	tenantLimit := &validation.Limits{}
	tenantLimits := &staticTenantLimits{limits: map[string]*validation.Limits{tenant: tenantLimit}}
	i := newOwnedScenarioIngester(t, me, "", tenantLimits, &desc, nil)
	t.Cleanup(i.closeAllTSDB)
	db, err := i.getOrCreateTSDB(tenant, true)
	require.NoError(t, err)

	next := 0
	series := ownedScenarioSeries(tenant, ownedTestSeries, &next, nil)
	for _, l := range series {
		require.NoError(t, appendOwnedTestSeries(db, l))
	}
	for _, size := range []int{ownedTestSmallShard, 0, ownedTestSmallShard, ownedTestLargeShard, 0} {
		tenantLimit.IngestionTenantShardSize = size
		i.ownershipRefresher.refresh(context.Background(), []string{tenant})
		want := countInRanges(tenant, series, ownedScenarioView(t, desc, tenant, size).ZonalOwnedRanges(me))
		assert.Equal(t, int64(want), db.ownedSeries.owned(), "shard size %d", size)
		assert.Equal(t, float64(want), testutil.ToFloat64(i.ownershipRefresher.ownedSeriesGauge.WithLabelValues(tenant)), "shard size %d", size)
	}
}

// An instance becoming READONLY hands its ranges to the next instance in its
// zone, which recounts, and takes them back when it is ACTIVE again.
func TestOwnedSeries_ReadOnlyNeighbourHandsOverRanges(t *testing.T) {
	const tenant = "tenant"
	me := ownershipTestInstanceID(0, 0)
	full := newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), ownedTestLayout)
	baseShare := ownedScenarioView(t, full, tenant, 0).ZonalOwnership()[me]
	var readOnly *ring.Desc
	for n := 1; n < ownedTestLayout.perZone; n++ {
		d := cloneOwnershipTestDesc(full)
		setOwnershipTestState(ownershipTestInstanceID(0, n), ring.READONLY)(d)
		if ownedScenarioView(t, d, tenant, 0).ZonalOwnership()[me] > baseShare {
			readOnly = d
			break
		}
	}
	require.NotNil(t, readOnly, "no zone-0 neighbour hands ranges to %s", me)

	desc := cloneOwnershipTestDesc(full)
	tenantLimits := &staticTenantLimits{limits: map[string]*validation.Limits{tenant: {}}}
	i := newOwnedScenarioIngester(t, me, "", tenantLimits, &desc, nil)
	t.Cleanup(i.closeAllTSDB)
	db, err := i.getOrCreateTSDB(tenant, true)
	require.NoError(t, err)
	next := 0
	series := ownedScenarioSeries(tenant, ownedTestSeries, &next, nil)
	for _, l := range series {
		require.NoError(t, appendOwnedTestSeries(db, l))
	}

	for step, d := range []*ring.Desc{full, readOnly, full} {
		desc = cloneOwnershipTestDesc(d)
		i.ownershipRefresher.refresh(context.Background(), []string{tenant})
		want := countInRanges(tenant, series, ownedScenarioView(t, d, tenant, 0).ZonalOwnedRanges(me))
		assert.Equal(t, int64(want), db.ownedSeries.owned(), "step %d", step)
	}
}

// Series created through the hooks while refreshes move the ranges back and
// forth leave the count matching the final ranges, with no recount needed.
func TestOwnedSeries_ConcurrentAppendsAcrossRingChanges(t *testing.T) {
	const (
		workers   = 8
		perWorker = 300
		refreshes = 40
	)
	s := newScaleUpScenario(t)
	tenantLimits := &staticTenantLimits{limits: map[string]*validation.Limits{s.tenant: {}}}
	desc := cloneOwnershipTestDesc(s.before)
	i := newOwnedScenarioIngester(t, s.me, "", tenantLimits, &desc, nil)
	t.Cleanup(i.closeAllTSDB)
	db, err := i.getOrCreateTSDB(s.tenant, true)
	require.NoError(t, err)
	i.ownershipRefresher.refresh(context.Background(), []string{s.tenant})

	var (
		wg      sync.WaitGroup
		mtx     sync.Mutex
		created []labels.Labels
	)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mine := make([]labels.Labels, 0, perWorker)
			for n := range perWorker {
				l := labels.FromStrings(labels.MetricName, "metric", "worker", fmt.Sprint(w), "series", fmt.Sprint(n))
				if appendOwnedTestSeries(db, l) == nil {
					mine = append(mine, l)
				}
			}
			mtx.Lock()
			created = append(created, mine...)
			mtx.Unlock()
		}()
	}
	// Only the refreshes swap the ring view, so they don't race on desc.
	final := s.before
	for n := range refreshes {
		final = s.before
		if n%2 == 0 {
			final = s.after
		}
		desc = cloneOwnershipTestDesc(final)
		i.ownershipRefresher.refresh(context.Background(), []string{s.tenant})
	}
	wg.Wait()

	require.Len(t, created, workers*perWorker)
	assert.Equal(t, int64(db.Head().NumSeries()), db.ownedSeries.total())
	want := countInRanges(s.tenant, created, ownedScenarioView(t, final, s.tenant, 0).ZonalOwnedRanges(s.me))
	assert.Equal(t, int64(want), db.ownedSeries.owned())
}

// Tracking needs all three settings; without any of them nothing is tracked.
func TestOwnedSeries_TrackingNeedsAllThreeSettings(t *testing.T) {
	const tenant = "tenant"
	desc := newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), ownedTestLayout)
	for name, mod := range map[string]func(*Config){
		"owned series limit disabled":     func(cfg *Config) { cfg.OwnedSeriesLimitEnabled = false },
		"ring ownership for limits off":   func(cfg *Config) { cfg.UseRingOwnershipForLimits = false },
		"distributor not shard-by-labels": func(cfg *Config) { cfg.DistributorShardByAllLabels = false },
	} {
		t.Run(name, func(t *testing.T) {
			tenantLimits := &staticTenantLimits{limits: map[string]*validation.Limits{tenant: {}}}
			i := newOwnedScenarioIngester(t, ownershipTestInstanceID(0, 0), "", tenantLimits, &desc, mod)
			t.Cleanup(i.closeAllTSDB)
			db, err := i.getOrCreateTSDB(tenant, true)
			require.NoError(t, err)
			assert.Nil(t, db.ownedSeries)
			if i.ownershipRefresher != nil {
				assert.Nil(t, i.ownershipRefresher.setOwnedRanges)
			}
			require.NoError(t, appendOwnedTestSeries(db, labels.FromStrings(labels.MetricName, "metric")))
		})
	}
}
