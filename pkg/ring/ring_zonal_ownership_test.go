package ring

import (
	"fmt"
	"maps"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	quarterOfRing        = uint32(1) << 30
	ownershipTolerance   = 1e-9
	routingSampleKeys    = 200_000
	routingTolerance     = 0.005
	ownershipTestSeed    = 42
	ownershipTestTimeout = time.Minute
)

type ownershipTestInstance struct {
	id     string
	zone   string
	tokens []uint32
	state  InstanceState
	stale  bool
}

func newOwnershipTestDesc(instances []ownershipTestInstance) *Desc {
	desc := NewDesc()
	for _, inst := range instances {
		ing := desc.AddIngester(inst.id, inst.id+"-addr", inst.zone, inst.tokens, inst.state, time.Time{})
		if inst.stale {
			ing.Timestamp = 0
			desc.Ingesters[inst.id] = ing
		}
	}
	return desc
}

func newOwnershipTestRing(t *testing.T, replicationFactor int, desc *Desc) *Ring {
	t.Helper()
	cfg := Config{
		HeartbeatTimeout:     ownershipTestTimeout,
		ReplicationFactor:    replicationFactor,
		ZoneAwarenessEnabled: true,
	}
	r, err := NewWithStoreClientAndStrategy(cfg, testRingName, testRingKey, &MockClient{}, NewDefaultReplicationStrategy(), prometheus.NewRegistry(), log.NewNopLogger())
	require.NoError(t, err)
	r.updateRingState(desc)
	return r
}

// Zone a: a1 owns (0, q], a2 owns (q, 2q], a3 owns (2q, 0] (wraps), so 1/4, 1/4, 1/2.
// Zone b: b1 owns (5q/2, q/2] (wraps), b2 owns (q/2, 5q/2], so 1/2 each.
func handPickedInstances(override map[string]func(*ownershipTestInstance)) []ownershipTestInstance {
	instances := []ownershipTestInstance{
		{id: "a1", zone: "a", tokens: []uint32{quarterOfRing}, state: ACTIVE},
		{id: "a2", zone: "a", tokens: []uint32{2 * quarterOfRing}, state: ACTIVE},
		{id: "a3", zone: "a", tokens: []uint32{0}, state: ACTIVE},
		{id: "b1", zone: "b", tokens: []uint32{quarterOfRing / 2}, state: ACTIVE},
		{id: "b2", zone: "b", tokens: []uint32{5 * (quarterOfRing / 2)}, state: ACTIVE},
	}
	for i := range instances {
		if f, ok := override[instances[i].id]; ok {
			f(&instances[i])
		}
	}
	return instances
}

func withState(s InstanceState) func(*ownershipTestInstance) {
	return func(i *ownershipTestInstance) { i.state = s }
}

func TestRing_ZonalOwnership_HandPickedTokens(t *testing.T) {
	balancedZoneB := map[string]float64{"b1": 0.5, "b2": 0.5}

	tests := map[string]struct {
		override map[string]func(*ownershipTestInstance)
		op       Operation
		expected map[string]float64
	}{
		"all ACTIVE": {
			op:       Write,
			expected: map[string]float64{"a1": 0.25, "a2": 0.25, "a3": 0.5},
		},
		"JOINING instance is left out with Write": {
			override: map[string]func(*ownershipTestInstance){"a2": withState(JOINING)},
			op:       Write,
			expected: map[string]float64{"a1": 0.25, "a2": 0, "a3": 0.75},
		},
		"JOINING instance keeps its range with WriteNoExtend": {
			override: map[string]func(*ownershipTestInstance){"a2": withState(JOINING)},
			op:       WriteNoExtend,
			expected: map[string]float64{"a1": 0.25, "a2": 0.25, "a3": 0.5},
		},
		"READONLY instance is left out with WriteNoExtend": {
			override: map[string]func(*ownershipTestInstance){"a2": withState(READONLY)},
			op:       WriteNoExtend,
			expected: map[string]float64{"a1": 0.25, "a2": 0, "a3": 0.75},
		},
		"only one instance left in the zone owns all of it": {
			override: map[string]func(*ownershipTestInstance){"a1": withState(LEAVING), "a2": withState(PENDING)},
			op:       Write,
			expected: map[string]float64{"a1": 0, "a2": 0, "a3": 1},
		},
		"every instance in the zone left out": {
			override: map[string]func(*ownershipTestInstance){
				"a1": withState(READONLY), "a2": withState(READONLY), "a3": withState(READONLY),
			},
			op:       Write,
			expected: map[string]float64{"a1": 0, "a2": 0, "a3": 0},
		},
		"ACTIVE instance with a stale heartbeat keeps its range": {
			override: map[string]func(*ownershipTestInstance){"a2": func(i *ownershipTestInstance) { i.stale = true }},
			op:       Write,
			expected: map[string]float64{"a1": 0.25, "a2": 0.25, "a3": 0.5},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := newOwnershipTestRing(t, 2, newOwnershipTestDesc(handPickedInstances(tc.override)))

			got := r.ZonalOwnership(tc.op)

			expected := map[string]float64{}
			maps.Copy(expected, balancedZoneB)
			maps.Copy(expected, tc.expected)
			require.Len(t, got, len(expected))
			for id, want := range expected {
				assert.InDelta(t, want, got[id], ownershipTolerance, "instance %s", id)
			}
		})
	}
}

func TestRing_ZonalOwnership_DuplicateTokenOwnsNoExtraRange(t *testing.T) {
	desc := newOwnershipTestDesc([]ownershipTestInstance{
		{id: "a1", zone: "a", tokens: []uint32{quarterOfRing, quarterOfRing}, state: ACTIVE},
		{id: "a2", zone: "a", tokens: []uint32{3 * quarterOfRing}, state: ACTIVE},
	})
	r := newOwnershipTestRing(t, 1, desc)

	got := r.ZonalOwnership(Write)

	assert.InDelta(t, 0.5, got["a1"], ownershipTolerance)
	assert.InDelta(t, 0.5, got["a2"], ownershipTolerance)
}

// randomOwnershipInstances builds a ring with random unique tokens. stateOf
// gives the state of instance i in zone z; nil means every instance is ACTIVE.
func randomOwnershipInstances(rnd *rand.Rand, zones, perZone, tokensPerInstance int, stateOf func(z, i int) InstanceState) []ownershipTestInstance {
	used := map[uint32]struct{}{}
	var instances []ownershipTestInstance
	for z := range zones {
		for i := range perZone {
			tokens := make([]uint32, 0, tokensPerInstance)
			for len(tokens) < tokensPerInstance {
				tok := rnd.Uint32()
				if _, dup := used[tok]; dup {
					continue
				}
				used[tok] = struct{}{}
				tokens = append(tokens, tok)
			}
			state := ACTIVE
			if stateOf != nil {
				state = stateOf(z, i)
			}
			instances = append(instances, ownershipTestInstance{
				id:     fmt.Sprintf("zone-%d-instance-%d", z, i),
				zone:   fmt.Sprintf("zone-%d", z),
				tokens: tokens,
				state:  state,
			})
		}
	}
	return instances
}

// joiningAndReadOnly puts the first instance of zone 0 in JOINING and the first
// instance of zone 1 in READONLY. Keeping them in different zones means every
// key still has a write quorum, with or without replica set extension.
func joiningAndReadOnly(z, i int) InstanceState {
	switch {
	case z == 0 && i == 0:
		return JOINING
	case z == 1 && i == 0:
		return READONLY
	default:
		return ACTIVE
	}
}

func TestRing_ZonalOwnership_SumsToOnePerZone(t *testing.T) {
	const (
		zones             = 3
		perZone           = 10
		tokensPerInstance = 128
	)
	rnd := rand.New(rand.NewSource(ownershipTestSeed))
	instances := randomOwnershipInstances(rnd, zones, perZone, tokensPerInstance, joiningAndReadOnly)
	r := newOwnershipTestRing(t, zones, newOwnershipTestDesc(instances))

	got := r.ZonalOwnership(Write)

	sumByZone := map[string]float64{}
	for _, inst := range instances {
		if inst.state != ACTIVE {
			assert.Zero(t, got[inst.id], "instance %s is %s", inst.id, inst.state)
		}
		sumByZone[inst.zone] += got[inst.id]
	}
	require.Len(t, sumByZone, zones)
	for zone, sum := range sumByZone {
		assert.InDelta(t, 1, sum, ownershipTolerance, "zone %s", zone)
	}
}

// The ownership must describe where Get actually sends keys: the fraction of
// random keys Get routes to an instance is its zonal ownership.
func TestRing_ZonalOwnership_MatchesWriteRouting(t *testing.T) {
	const (
		zones             = 3
		perZone           = 6
		tokensPerInstance = 16
	)

	for name, op := range map[string]Operation{"Write": Write, "WriteNoExtend": WriteNoExtend} {
		t.Run(name, func(t *testing.T) {
			rnd := rand.New(rand.NewSource(ownershipTestSeed))
			instances := randomOwnershipInstances(rnd, zones, perZone, tokensPerInstance, joiningAndReadOnly)
			r := newOwnershipTestRing(t, zones, newOwnershipTestDesc(instances))
			ownership := r.ZonalOwnership(op)

			idByAddr := map[string]string{}
			for _, inst := range instances {
				idByAddr[inst.id+"-addr"] = inst.id
			}

			received := map[string]int{}
			bufDescs, bufHosts, bufZones := MakeBuffersForGet()
			for range routingSampleKeys {
				set, err := r.Get(rnd.Uint32(), op, bufDescs, bufHosts, bufZones)
				require.NoError(t, err)
				for _, ing := range set.Instances {
					received[idByAddr[ing.Addr]]++
				}
			}

			for _, inst := range instances {
				fraction := float64(received[inst.id]) / routingSampleKeys
				if inst.state != ACTIVE {
					// Filter never sends writes to non-ACTIVE instances, whatever
					// ownership says about the range they hold.
					assert.Zero(t, received[inst.id], "instance %s is %s", inst.id, inst.state)
					continue
				}
				assert.InDelta(t, ownership[inst.id], fraction, routingTolerance, "instance %s", inst.id)
			}
		})
	}
}

func TestRing_ZonalOwnership_Cached(t *testing.T) {
	r := newOwnershipTestRing(t, 2, newOwnershipTestDesc(handPickedInstances(nil)))

	first := r.ZonalOwnership(Write)
	second := r.ZonalOwnership(Write)

	assert.Equal(t, reflect.ValueOf(first).Pointer(), reflect.ValueOf(second).Pointer())
}

func TestRing_ZonalOwnership_InvalidatedOnStateChange(t *testing.T) {
	r := newOwnershipTestRing(t, 2, newOwnershipTestDesc(handPickedInstances(nil)))
	require.InDelta(t, 0.25, r.ZonalOwnership(Write)["a2"], ownershipTolerance)

	// Same tokens, different state: updateRingState takes its fast path.
	r.updateRingState(newOwnershipTestDesc(handPickedInstances(map[string]func(*ownershipTestInstance){"a2": withState(JOINING)})))

	got := r.ZonalOwnership(Write)
	assert.Zero(t, got["a2"])
	assert.InDelta(t, 0.75, got["a3"], ownershipTolerance)
}

func TestRing_ZonalOwnership_KeptOnHeartbeat(t *testing.T) {
	r := newOwnershipTestRing(t, 2, newOwnershipTestDesc(handPickedInstances(nil)))
	first := r.ZonalOwnership(Write)

	// Same tokens and states, newer heartbeats: only timestamps change.
	desc := newOwnershipTestDesc(handPickedInstances(nil))
	for id, instance := range desc.Ingesters {
		instance.Timestamp++
		desc.Ingesters[id] = instance
	}
	r.updateRingState(desc)

	assert.Equal(t, reflect.ValueOf(first).Pointer(), reflect.ValueOf(r.ZonalOwnership(Write)).Pointer(),
		"a heartbeat must not invalidate the cached ownership")
}

func TestRing_ZonalOwnership_InvalidatedOnTokenChange(t *testing.T) {
	r := newOwnershipTestRing(t, 2, newOwnershipTestDesc(handPickedInstances(nil)))
	require.InDelta(t, 0.25, r.ZonalOwnership(Write)["a1"], ownershipTolerance)

	// Moving a1's token from q to q/4 shrinks its range to 1/16 and grows a2's.
	r.updateRingState(newOwnershipTestDesc(handPickedInstances(map[string]func(*ownershipTestInstance){
		"a1": func(i *ownershipTestInstance) { i.tokens = []uint32{quarterOfRing / 4} },
	})))

	got := r.ZonalOwnership(Write)
	assert.InDelta(t, 0.0625, got["a1"], ownershipTolerance)
	assert.InDelta(t, 0.4375, got["a2"], ownershipTolerance)
}

func TestRing_ZonalOwnership_CachedSubringInvalidatedOnStateChange(t *testing.T) {
	const (
		zones             = 2
		perZone           = 4
		tokensPerInstance = 32
		shardSize         = 4
		tenant            = "tenant"
	)
	rnd := rand.New(rand.NewSource(ownershipTestSeed))
	instances := randomOwnershipInstances(rnd, zones, perZone, tokensPerInstance, nil)
	r := newOwnershipTestRing(t, zones, newOwnershipTestDesc(instances))

	subring := r.ShuffleShard(tenant, shardSize)
	subOwnership := subring.(ZonalOwnershipRing).ZonalOwnership(Write)
	require.Len(t, subOwnership, shardSize)

	sumByZone := map[string]float64{}
	var target string
	for _, inst := range instances {
		share, inShard := subOwnership[inst.id]
		if !inShard {
			continue
		}
		sumByZone[inst.zone] += share
		if target == "" {
			target = inst.id
		}
	}
	for zone, sum := range sumByZone {
		assert.InDelta(t, 1, sum, ownershipTolerance, "zone %s", zone)
	}
	require.Positive(t, subOwnership[target])

	// Change only the target's state in the parent ring. The subring is served
	// from the cache with refreshed states, so its ownership must be recomputed.
	for i := range instances {
		if instances[i].id == target {
			instances[i].state = JOINING
		}
	}
	r.updateRingState(newOwnershipTestDesc(instances))

	cachedSubring := r.ShuffleShard(tenant, shardSize)
	require.Same(t, subring, cachedSubring, "expected the cached subring")
	assert.Zero(t, cachedSubring.(ZonalOwnershipRing).ZonalOwnership(Write)[target])
}

// cachedOwnershipPointer returns the identity of the map cached for op, so a
// test can tell whether a call reused the cache or recomputed it.
func cachedOwnershipPointer(r *Ring, op Operation) uintptr {
	return reflect.ValueOf(r.ZonalOwnership(op)).Pointer()
}

func TestRing_ZonalOwnership_InvalidatedOnlyWhenOwnershipCanChange(t *testing.T) {
	tests := map[string]struct {
		from, to                     InstanceState
		writeKept, writeNoExtendKept bool
	}{
		// Write extends past every state but ACTIVE, WriteNoExtend only past READONLY.
		"ACTIVE to LEAVING":   {from: ACTIVE, to: LEAVING, writeKept: false, writeNoExtendKept: true},
		"ACTIVE to READONLY":  {from: ACTIVE, to: READONLY, writeKept: false, writeNoExtendKept: false},
		"JOINING to ACTIVE":   {from: JOINING, to: ACTIVE, writeKept: false, writeNoExtendKept: true},
		"PENDING to JOINING":  {from: PENDING, to: JOINING, writeKept: true, writeNoExtendKept: true},
		"LEAVING to READONLY": {from: LEAVING, to: READONLY, writeKept: true, writeNoExtendKept: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := newOwnershipTestRing(t, 2, newOwnershipTestDesc(handPickedInstances(map[string]func(*ownershipTestInstance){"a2": withState(tc.from)})))
			write, writeNoExtend := cachedOwnershipPointer(r, Write), cachedOwnershipPointer(r, WriteNoExtend)

			r.updateRingState(newOwnershipTestDesc(handPickedInstances(map[string]func(*ownershipTestInstance){"a2": withState(tc.to)})))

			assert.Equal(t, tc.writeKept, cachedOwnershipPointer(r, Write) == write, "Write cache kept")
			assert.Equal(t, tc.writeNoExtendKept, cachedOwnershipPointer(r, WriteNoExtend) == writeNoExtend, "WriteNoExtend cache kept")
		})
	}
}

func TestRing_ZonalOwnership_InvalidatedWhenAnInstanceLeavesTheRing(t *testing.T) {
	r := newOwnershipTestRing(t, 2, newOwnershipTestDesc(handPickedInstances(nil)))
	require.InDelta(t, 0.25, r.ZonalOwnership(Write)["a2"], ownershipTolerance)

	desc := newOwnershipTestDesc(handPickedInstances(nil))
	desc.RemoveIngester("a2")
	r.updateRingState(desc)

	got := r.ZonalOwnership(Write)
	assert.NotContains(t, got, "a2")
	assert.InDelta(t, 0.75, got["a3"], ownershipTolerance)
}

// Whatever sequence of heartbeats and state changes a ring goes through, the
// cached ownership must equal a fresh computation, for the whole ring and for
// cached shuffle-shard subrings.
func TestRing_ZonalOwnership_CacheMatchesFreshComputation(t *testing.T) {
	const (
		zones     = 3
		perZone   = 6
		tokens    = 16
		steps     = 300
		shardSize = 9
		tenant    = "tenant"
	)
	states := []InstanceState{ACTIVE, ACTIVE, ACTIVE, JOINING, LEAVING, PENDING, READONLY}
	ops := map[string]Operation{"Write": Write, "WriteNoExtend": WriteNoExtend}
	rnd := rand.New(rand.NewSource(ownershipTestSeed))

	desc := newOwnershipTestDesc(randomOwnershipInstances(rnd, zones, perZone, tokens, nil))
	r := newOwnershipTestRing(t, zones, desc)
	ids := make([]string, 0, len(desc.Ingesters))
	for id := range desc.Ingesters {
		ids = append(ids, id)
	}

	fresh := func(d *Desc) *Ring {
		f, err := NewDescFedRing(Config{HeartbeatTimeout: ownershipTestTimeout, ReplicationFactor: zones, ZoneAwarenessEnabled: true},
			testRingName, testRingKey, &MockClient{}, log.NewNopLogger())
		require.NoError(t, err)
		f.UpdateDesc(d)
		return f
	}

	for step := range steps {
		// Every step is a heartbeat; some also change one instance's state.
		next := newOwnershipTestDesc(nil)
		for id, instance := range desc.Ingesters {
			instance.Timestamp++
			next.Ingesters[id] = instance
		}
		if rnd.Intn(2) == 0 {
			id := ids[rnd.Intn(len(ids))]
			instance := next.Ingesters[id]
			instance.State = states[rnd.Intn(len(states))]
			next.Ingesters[id] = instance
		}
		desc = next
		r.UpdateDesc(desc)

		want := fresh(desc)
		for name, op := range ops {
			require.Equal(t, want.ZonalOwnership(op), r.ZonalOwnership(op), "step %d, whole ring, %s", step, name)
			require.Equal(t,
				want.ShuffleShard(tenant, shardSize).(*Ring).ZonalOwnership(op),
				r.ShuffleShard(tenant, shardSize).(*Ring).ZonalOwnership(op),
				"step %d, subring, %s", step, name)
		}
	}
}
