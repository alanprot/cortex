package ingester

import (
	"fmt"
	"maps"
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cortexproject/cortex/pkg/ring"
	"github.com/cortexproject/cortex/pkg/ring/kv/consul"
	"github.com/cortexproject/cortex/pkg/util"
	"github.com/cortexproject/cortex/pkg/util/validation"
)

const (
	ownershipTestSeed      = 7
	ownershipTestTolerance = 1e-9
	ownershipTestRingKey   = "ownership-test"
)

type ownershipTestRingLayout struct {
	zones             int
	perZone           int
	tokensPerInstance int
}

func ownershipTestInstanceID(zone, instance int) string {
	return fmt.Sprintf("zone-%d-ingester-%d", zone, instance)
}

// newOwnershipTestDesc returns a ring with random tokens. Few tokens per
// instance make ownership uneven, as it is in a real ring.
func newOwnershipTestDesc(rnd *rand.Rand, layout ownershipTestRingLayout) *ring.Desc {
	desc := ring.NewDesc()
	used := map[uint32]struct{}{}
	for z := range layout.zones {
		for i := range layout.perZone {
			tokens := make([]uint32, 0, layout.tokensPerInstance)
			for len(tokens) < layout.tokensPerInstance {
				tok := rnd.Uint32()
				if _, dup := used[tok]; dup {
					continue
				}
				used[tok] = struct{}{}
				tokens = append(tokens, tok)
			}
			slices.Sort(tokens)
			id := ownershipTestInstanceID(z, i)
			desc.AddIngester(id, id+"-addr", fmt.Sprintf("zone-%d", z), tokens, ring.ACTIVE, time.Time{})
		}
	}
	return desc
}

func newOwnershipTestRing(t *testing.T, replicationFactor int, excludedZones []string) *ring.Ring {
	t.Helper()
	kvClient, closer := consul.NewInMemoryClient(ring.GetCodec(), log.NewNopLogger(), nil)
	t.Cleanup(func() { _ = closer.Close() })

	cfg := ring.Config{
		HeartbeatTimeout:     time.Minute,
		ReplicationFactor:    replicationFactor,
		ZoneAwarenessEnabled: true,
		ExcludedZones:        excludedZones,
	}
	r, err := ring.NewDescFedRing(cfg, "ingester-ownership", ownershipTestRingKey, kvClient, log.NewNopLogger())
	require.NoError(t, err)
	return r
}

func TestRingOwnershipDelegate_FeedsACopyOfTheDesc(t *testing.T) {
	desc := ring.NewDesc()
	desc.AddIngester("a-1", "a-1-addr", "a", []uint32{300, 100}, ring.ACTIVE, time.Time{})
	desc.AddIngester("a-2", "a-2-addr", "a", []uint32{200}, ring.ACTIVE, time.Time{})
	desc.AddIngester("b-1", "b-1-addr", "b", []uint32{400}, ring.ACTIVE, time.Time{})

	r := newOwnershipTestRing(t, 1, []string{"b"})
	delegate := &ringOwnershipDelegate{}
	delegate.ring.Store(r)

	delegate.OnRingInstanceHeartbeat(nil, desc)

	// The ring deletes excluded zones and sorts tokens in place; the desc being
	// written to the KV store must be left as it was.
	assert.Contains(t, desc.Ingesters, "b-1")
	assert.Equal(t, []uint32{300, 100}, desc.Ingesters["a-1"].Tokens)

	ownership := r.ZonalOwnership(ring.Write)
	assert.NotContains(t, ownership, "b-1")
	assert.Positive(t, ownership["a-1"])
	assert.Positive(t, ownership["a-2"])
}

func TestRingOwnershipDelegate_NoRing(t *testing.T) {
	delegate := &ringOwnershipDelegate{}
	assert.NotPanics(t, func() { delegate.OnRingInstanceHeartbeat(nil, ring.NewDesc()) })
}

func TestOwnershipRefresher(t *testing.T) {
	const (
		shuffledTenant   = "shuffled"
		unshuffledTenant = "unshuffled"
		shardSize        = 6
	)
	layout := ownershipTestRingLayout{zones: 3, perZone: 4, tokensPerInstance: 16}
	desc := newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), layout)

	r := newOwnershipTestRing(t, layout.zones, nil)
	limits := validation.Limits{}
	tenantLimits := &staticTenantLimits{limits: map[string]*validation.Limits{
		shuffledTenant: {IngestionTenantShardSize: shardSize},
	}}
	overrides := validation.NewOverrides(limits, tenantLimits)

	ingesterID := ownershipTestInstanceID(0, 0)
	refresher := newOwnershipRefresher(r, ingesterID, true, true, overrides, nil, nil)

	// Nothing is known before the ring has been fed.
	refresher.refresh([]string{shuffledTenant, unshuffledTenant})
	_, ok := refresher.ownership.share(unshuffledTenant)
	assert.False(t, ok)

	r.UpdateDesc(desc)
	refresher.refresh([]string{shuffledTenant, unshuffledTenant})

	share, ok := refresher.ownership.share(unshuffledTenant)
	require.True(t, ok)
	assert.InDelta(t, r.ZonalOwnership(ring.Write)[ingesterID], share, ownershipTestTolerance)

	subOwnership := r.ShuffleShard(shuffledTenant, shardSize).(ring.ZonalOwnershipRing).ZonalOwnership(ring.Write)
	share, ok = refresher.ownership.share(shuffledTenant)
	if want, inShard := subOwnership[ingesterID]; inShard {
		require.True(t, ok)
		assert.InDelta(t, want, share, ownershipTestTolerance)
	} else {
		assert.False(t, ok, "an ingester outside the tenant's shard has no share")
	}

	_, ok = refresher.ownership.share("unknown-tenant")
	assert.False(t, ok)
}

func TestOwnershipRefresher_ZoneMissingFromRing(t *testing.T) {
	const tenant = "tenant"
	// RF 3 over a ring with only 2 zones: Get puts a second copy in one zone,
	// which zonal shares don't describe.
	layout := ownershipTestRingLayout{zones: 2, perZone: 4, tokensPerInstance: 16}
	r := newOwnershipTestRing(t, 3, nil)
	r.UpdateDesc(newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), layout))

	refresher := newOwnershipRefresher(r, ownershipTestInstanceID(0, 0), true, false, validation.NewOverrides(validation.Limits{}, nil), nil, nil)
	refresher.refresh([]string{tenant})

	_, ok := refresher.ownership.share(tenant)
	assert.False(t, ok)
}

func TestConfig_RingOwnershipRequiresZoneAwareness(t *testing.T) {
	cfg := defaultIngesterTestConfig(t)
	cfg.UseRingOwnershipForLimits = true

	cfg.LifecyclerConfig.RingConfig.ZoneAwarenessEnabled = false
	assert.ErrorIs(t, cfg.Validate(nil), errRingOwnershipRequiresZoneAwareness)

	cfg.LifecyclerConfig.RingConfig.ZoneAwarenessEnabled = true
	assert.NoError(t, cfg.Validate(nil))
}

func TestLimiter_LocalLimitFromOwnership(t *testing.T) {
	const (
		globalLimit       = 1000
		replicationFactor = 3
		ingesters         = 30
		zones             = 3
		tenant            = "tenant"
	)
	// The existing formula: 1000 / 30 ingesters * RF 3.
	const evenSplitLimit = 100

	tests := map[string]struct {
		share             float64
		zoneAware         bool
		shardByAllLabels  bool
		expectedPerUser   int
		expectedPerMetric int
	}{
		"share is used": {
			share: 0.15, zoneAware: true, shardByAllLabels: true,
			expectedPerUser: 150, expectedPerMetric: 150,
		},
		"no share falls back to the even split": {
			zoneAware: true, shardByAllLabels: true,
			expectedPerUser: evenSplitLimit, expectedPerMetric: evenSplitLimit,
		},
		"share is ignored without zone awareness": {
			share: 0.15, shardByAllLabels: true,
			expectedPerUser: evenSplitLimit, expectedPerMetric: evenSplitLimit,
		},
		"per-metric limit is not divided without shard-by-all-labels": {
			share: 0.15, zoneAware: true,
			expectedPerUser: math.MaxInt32, expectedPerMetric: globalLimit,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ringCount := &ringCountMock{}
			ringCount.On("HealthyInstancesCount").Return(ingesters)
			ringCount.On("ZonesCount").Return(zones)
			overrides := validation.NewOverrides(validation.Limits{
				MaxGlobalSeriesPerUser:   globalLimit,
				MaxGlobalSeriesPerMetric: globalLimit,
			}, nil)

			limiter := NewLimiter(overrides, ringCount, util.ShardingStrategyDefault, tc.shardByAllLabels, replicationFactor, tc.zoneAware, "")
			ownership := &tenantOwnership{}
			if tc.share > 0 {
				ownership.shares.Store(&map[string]float64{tenant: tc.share})
			}
			limiter.ownership = ownership

			assert.Equal(t, tc.expectedPerUser, limiter.maxSeriesPerUser(tenant))
			assert.Equal(t, tc.expectedPerMetric, limiter.maxSeriesPerMetric(tenant))
		})
	}
}

// A tenant at 95% of its global limit, on a ring whose ownership is uneven:
// the even-split limit throttles it on the ingesters with the largest share,
// while the ownership-based limit does not throttle it anywhere.
func TestLimiter_OwnershipAvoidsThrottlingOnUnevenRing(t *testing.T) {
	const (
		globalLimit = 100_000
		pushed      = 95_000
		tenant      = "tenant"
	)
	layout := ownershipTestRingLayout{zones: 3, perZone: 4, tokensPerInstance: 16}
	rnd := rand.New(rand.NewSource(ownershipTestSeed))
	r := newOwnershipTestRing(t, layout.zones, nil)
	r.UpdateDesc(newOwnershipTestDesc(rnd, layout))

	// Route the tenant's series the way the distributor does.
	received := map[string]int{}
	bufDescs, bufHosts, bufZones := ring.MakeBuffersForGet()
	for range pushed {
		set, err := r.Get(rnd.Uint32(), ring.Write, bufDescs, bufHosts, bufZones)
		require.NoError(t, err)
		for _, inst := range set.Instances {
			received[inst.Addr]++
		}
	}

	ringCount := &ringCountMock{}
	ringCount.On("HealthyInstancesCount").Return(layout.zones * layout.perZone)
	ringCount.On("ZonesCount").Return(layout.zones)
	overrides := validation.NewOverrides(validation.Limits{MaxGlobalSeriesPerUser: globalLimit}, nil)
	evenSplit := NewLimiter(overrides, ringCount, util.ShardingStrategyDefault, true, layout.zones, true, "")

	throttledByEvenSplit := 0
	for z := range layout.zones {
		for i := range layout.perZone {
			id := ownershipTestInstanceID(z, i)
			series := received[id+"-addr"]

			if evenSplit.AssertMaxSeriesPerUser(tenant, series) != nil {
				throttledByEvenSplit++
			}

			refresher := newOwnershipRefresher(r, id, true, false, overrides, nil, nil)
			refresher.refresh([]string{tenant})
			byOwnership := NewLimiter(overrides, ringCount, util.ShardingStrategyDefault, true, layout.zones, true, "")
			byOwnership.ownership = refresher.ownership
			assert.NoError(t, byOwnership.AssertMaxSeriesPerUser(tenant, series), "ingester %s with %d series", id, series)
		}
	}
	t.Logf("throttled by even split on %d of %d ingesters", throttledByEvenSplit, layout.zones*layout.perZone)
	assert.Positive(t, throttledByEvenSplit, "expected the even split to throttle the tenant on this ring")
}

type staticTenantLimits struct {
	limits map[string]*validation.Limits
}

func (s *staticTenantLimits) ByUserID(userID string) *validation.Limits {
	return s.limits[userID]
}

func (s *staticTenantLimits) AllByUserID() map[string]*validation.Limits {
	return s.limits
}

func newOwnershipTestDelegate(t *testing.T, instanceID string, replicationFactor int, extendWrites bool) (*ringOwnershipDelegate, *ring.Ring) {
	t.Helper()
	kvClient, closer := consul.NewInMemoryClient(ring.GetCodec(), log.NewNopLogger(), nil)
	t.Cleanup(func() { _ = closer.Close() })
	r, err := newOwnershipRing(ring.Config{HeartbeatTimeout: time.Minute, ReplicationFactor: replicationFactor, ZoneAwarenessEnabled: true}, kvClient, log.NewNopLogger())
	require.NoError(t, err)

	d := &ringOwnershipDelegate{instanceID: instanceID, writeOp: ownershipWriteOp(extendWrites)}
	d.ring.Store(r)
	return d, r
}

func TestNewOwnershipRing_DoesNotCacheSubrings(t *testing.T) {
	layout := ownershipTestRingLayout{zones: 3, perZone: 4, tokensPerInstance: 16}
	d, r := newOwnershipTestDelegate(t, ownershipTestInstanceID(0, 0), layout.zones, true)
	d.OnRingInstanceHeartbeat(nil, newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), layout))

	assert.NotSame(t, r.ShuffleShard("tenant", 6), r.ShuffleShard("tenant", 6))
}

func TestRingOwnershipDelegate_Generation(t *testing.T) {
	me := ownershipTestInstanceID(0, 0)
	layout := ownershipTestRingLayout{zones: 3, perZone: 3, tokensPerInstance: 4}
	base := func() *ring.Desc { return newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), layout) }
	setState := func(id string, state ring.InstanceState) func(*ring.Desc) {
		return func(d *ring.Desc) {
			instance := d.Ingesters[id]
			instance.State = state
			d.Ingesters[id] = instance
		}
	}

	tests := map[string]struct {
		prepare        func(*ring.Desc)
		change         func(*ring.Desc)
		noExtendWrites bool
		bumped         bool
	}{
		"own zone, Write still extends past it": {
			prepare: setState(ownershipTestInstanceID(0, 1), ring.PENDING),
			change:  setState(ownershipTestInstanceID(0, 1), ring.JOINING),
		},
		"own zone, WriteNoExtend still stops at it": {
			change: setState(ownershipTestInstanceID(0, 1), ring.LEAVING), noExtendWrites: true,
		},
		"own zone, WriteNoExtend now extends past it": {
			change: setState(ownershipTestInstanceID(0, 1), ring.READONLY), noExtendWrites: true, bumped: true,
		},
		"own zone, READONLY changes the shard pick": {
			prepare: setState(ownershipTestInstanceID(0, 1), ring.LEAVING),
			change:  setState(ownershipTestInstanceID(0, 1), ring.READONLY), bumped: true,
		},
		"heartbeat only": {change: func(d *ring.Desc) {
			for id, instance := range d.Ingesters {
				instance.Timestamp++
				d.Ingesters[id] = instance
			}
		}},
		"state change in another zone": {change: setState(ownershipTestInstanceID(1, 0), ring.LEAVING)},
		"READONLY in another zone":     {change: setState(ownershipTestInstanceID(2, 1), ring.READONLY)},
		"state change in its own zone": {change: setState(ownershipTestInstanceID(0, 1), ring.LEAVING), bumped: true},
		"its own state change":         {change: setState(me, ring.LEAVING), bumped: true},
		"instance leaves another zone": {change: func(d *ring.Desc) { d.RemoveIngester(ownershipTestInstanceID(1, 0)) }, bumped: true},
		"instance joins another zone":  {change: func(d *ring.Desc) { d.AddIngester("new", "new", "zone-1", []uint32{42}, ring.ACTIVE, time.Time{}) }, bumped: true},
		"tokens change in another zone": {change: func(d *ring.Desc) {
			instance := d.Ingesters[ownershipTestInstanceID(2, 0)]
			instance.Tokens = append(slices.Clone(instance.Tokens[1:]), instance.Tokens[0]+1)
			d.Ingesters[ownershipTestInstanceID(2, 0)] = instance
		}, bumped: true},
		"state change in another zone that shares a token": {
			prepare: func(d *ring.Desc) {
				instance := d.Ingesters[ownershipTestInstanceID(1, 0)]
				instance.Tokens = append(slices.Clone(instance.Tokens), d.Ingesters[me].Tokens[0])
				d.Ingesters[ownershipTestInstanceID(1, 0)] = instance
			},
			change: setState(ownershipTestInstanceID(1, 0), ring.LEAVING),
			bumped: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := newOwnershipTestDelegate(t, me, layout.zones, !tc.noExtendWrites)
			desc := base()
			if tc.prepare != nil {
				tc.prepare(desc)
			}
			d.OnRingInstanceHeartbeat(nil, desc)
			require.Equal(t, uint64(1), d.generation.Load(), "the first ring is a change")

			next := copyRingDesc(desc)
			tc.change(next)
			d.OnRingInstanceHeartbeat(nil, next)

			assert.Equal(t, tc.bumped, d.generation.Load() != 1)
		})
	}
}

func TestOwnershipRefresher_ReusesSharesUntilTheRingChangesThem(t *testing.T) {
	const (
		tenant    = "tenant"
		shardSize = 6
	)
	me := ownershipTestInstanceID(0, 0)
	layout := ownershipTestRingLayout{zones: 3, perZone: 4, tokensPerInstance: 16}
	desc := newOwnershipTestDesc(rand.New(rand.NewSource(ownershipTestSeed)), layout)
	d, r := newOwnershipTestDelegate(t, me, layout.zones, true)
	d.OnRingInstanceHeartbeat(nil, desc)

	limits := &staticTenantLimits{limits: map[string]*validation.Limits{tenant: {IngestionTenantShardSize: shardSize}}}
	refresher := newOwnershipRefresher(r, me, true, true, validation.NewOverrides(validation.Limits{}, limits), nil, d.generation.Load)
	refresher.refresh([]string{tenant})
	first := refresher.tenants[tenant]
	wantShare := func(size int) float64 {
		return r.ShuffleShard(tenant, size).(ring.ZonalOwnershipRing).ZonalOwnership(ring.Write)[me]
	}
	require.Equal(t, wantShare(shardSize), first.share)

	setState := func(id string, state ring.InstanceState) {
		instance := desc.Ingesters[id]
		instance.State = state
		desc.Ingesters[id] = instance
		d.OnRingInstanceHeartbeat(nil, desc)
		refresher.refresh([]string{tenant})
	}

	setState(ownershipTestInstanceID(1, 0), ring.LEAVING)
	assert.Equal(t, first, refresher.tenants[tenant], "a state change in another zone keeps the share")

	setState(ownershipTestInstanceID(0, 1), ring.LEAVING)
	assert.NotEqual(t, first.generation, refresher.tenants[tenant].generation, "a state change in its own zone recomputes")
	assert.Equal(t, wantShare(shardSize), refresher.tenants[tenant].share)

	limits.limits[tenant] = &validation.Limits{IngestionTenantShardSize: shardSize + 3}
	refresher.refresh([]string{tenant})
	assert.Equal(t, shardSize+3, refresher.tenants[tenant].shardSize)
	assert.Equal(t, wantShare(shardSize+3), refresher.tenants[tenant].share)

	refresher.refresh(nil)
	assert.Empty(t, refresher.tenants, "a tenant gone from the ingester is dropped")
}

// Through any sequence of heartbeats, state changes, joins and leaves, the
// shares a refresher reuses must equal those computed from scratch.
func TestOwnershipRefresher_CachedSharesMatchFreshComputation(t *testing.T) {
	for _, extendWrites := range []bool{true, false} {
		t.Run(fmt.Sprintf("extend writes %t", extendWrites), func(t *testing.T) {
			testOwnershipRefresherCachedSharesMatchFreshComputation(t, extendWrites)
		})
	}
}

func testOwnershipRefresherCachedSharesMatchFreshComputation(t *testing.T, extendWrites bool) {
	const steps = 300
	states := []ring.InstanceState{ring.ACTIVE, ring.ACTIVE, ring.ACTIVE, ring.JOINING, ring.LEAVING, ring.PENDING, ring.READONLY}
	me := ownershipTestInstanceID(0, 0)
	rnd := rand.New(rand.NewSource(ownershipTestSeed))
	layout := ownershipTestRingLayout{zones: 3, perZone: 5, tokensPerInstance: 8}
	desc := newOwnershipTestDesc(rnd, layout)

	tenantLimits := map[string]*validation.Limits{}
	var tenants []string
	for i, size := range []int{0, 3, 6, 9, 30} {
		tenant := fmt.Sprintf("tenant-%d", i)
		tenants = append(tenants, tenant)
		tenantLimits[tenant] = &validation.Limits{IngestionTenantShardSize: size}
	}
	overrides := validation.NewOverrides(validation.Limits{}, &staticTenantLimits{limits: tenantLimits})

	d, r := newOwnershipTestDelegate(t, me, layout.zones, extendWrites)
	refresher := newOwnershipRefresher(r, me, extendWrites, true, overrides, nil, d.generation.Load)
	joined := 0
	recomputes := 0

	for step := range steps {
		next := copyRingDesc(desc)
		for id, instance := range next.Ingesters {
			instance.Timestamp++
			next.Ingesters[id] = instance
		}
		ids := slices.Sorted(maps.Keys(next.Ingesters))
		switch rnd.Intn(10) {
		case 0:
			if id := ids[rnd.Intn(len(ids))]; id != me {
				next.RemoveIngester(id)
			}
		case 1:
			joined++
			zone := fmt.Sprintf("zone-%d", rnd.Intn(layout.zones))
			next.AddIngester(fmt.Sprintf("joined-%d", joined), "addr", zone, []uint32{rnd.Uint32(), rnd.Uint32()}, ring.JOINING, time.Time{})
		case 2, 3, 4, 5:
			id := ids[rnd.Intn(len(ids))]
			instance := next.Ingesters[id]
			instance.State = states[rnd.Intn(len(states))]
			next.Ingesters[id] = instance
		}
		desc = next
		before := d.generation.Load()
		d.OnRingInstanceHeartbeat(nil, desc)
		if d.generation.Load() != before {
			recomputes++
		}
		refresher.refresh(tenants)

		fresh := newOwnershipRefresher(r, me, extendWrites, true, overrides, nil, nil)
		fresh.refresh(tenants)
		require.Equal(t, fresh.tenants, withoutGeneration(refresher.tenants), "step %d", step)
	}
	// Not a correctness condition: shows the test exercises reuse.
	t.Logf("recomputed on %d of %d steps", recomputes, steps)
	assert.Less(t, recomputes, steps)
}

func withoutGeneration(tenants map[string]tenantShare) map[string]tenantShare {
	out := make(map[string]tenantShare, len(tenants))
	for id, t := range tenants {
		t.generation = 0
		out[id] = t
	}
	return out
}

// A neighbour going from LEAVING to READONLY does not change whether Write
// extends past it, but shard picks skip READONLY instances, so the tenant's
// shard and this ingester's share change. On this layout the share goes from 0
// to about 0.54.
func TestOwnershipRefresher_ReadOnlyNeighbourChangesTheShardPick(t *testing.T) {
	// The tenant name and the seed below pick a layout where this happens.
	const tenant = "t"
	me := ownershipTestInstanceID(0, 0)
	neighbour := ownershipTestInstanceID(0, 1)
	layout := ownershipTestRingLayout{zones: 3, perZone: 6, tokensPerInstance: 8}
	desc := newOwnershipTestDesc(rand.New(rand.NewSource(3)), layout)
	setNeighbour := func(state ring.InstanceState) {
		instance := desc.Ingesters[neighbour]
		instance.State = state
		desc.Ingesters[neighbour] = instance
	}
	limits := &staticTenantLimits{limits: map[string]*validation.Limits{tenant: {IngestionTenantShardSize: 6}}}
	overrides := validation.NewOverrides(validation.Limits{}, limits)

	setNeighbour(ring.LEAVING)
	d, r := newOwnershipTestDelegate(t, me, layout.zones, true)
	d.OnRingInstanceHeartbeat(nil, desc)
	refresher := newOwnershipRefresher(r, me, true, true, overrides, nil, d.generation.Load)
	refresher.refresh([]string{tenant})
	before := refresher.tenants[tenant].share

	setNeighbour(ring.READONLY)
	d.OnRingInstanceHeartbeat(nil, copyRingDesc(desc))
	refresher.refresh([]string{tenant})

	fresh := newOwnershipRefresher(r, me, true, true, overrides, nil, nil)
	fresh.refresh([]string{tenant})
	require.NotEqual(t, before, fresh.tenants[tenant].share, "the layout must make the pick change")
	assert.Equal(t, fresh.tenants[tenant].share, refresher.tenants[tenant].share)
}
