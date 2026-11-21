package ring

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var shardOwnershipTestStates = []InstanceState{ACTIVE, ACTIVE, ACTIVE, ACTIVE, JOINING, LEAVING, PENDING, READONLY}

// randomShardOwnershipDesc returns a ring with uneven zones, random states and
// random unique tokens.
func randomShardOwnershipDesc(rnd *rand.Rand) *Desc {
	desc := NewDesc()
	used := map[uint32]struct{}{}
	zones := 2 + rnd.Intn(3)
	for z := range zones {
		perZone := 1 + rnd.Intn(12)
		for i := range perZone {
			numTokens := 1 + rnd.Intn(64)
			tokens := make([]uint32, 0, numTokens)
			for len(tokens) < numTokens {
				token := rnd.Uint32()
				if _, ok := used[token]; ok {
					continue
				}
				used[token] = struct{}{}
				tokens = append(tokens, token)
			}
			id := fmt.Sprintf("z%d-i%d", z, i)
			desc.AddIngester(id, id, fmt.Sprintf("zone-%d", z), tokens, shardOwnershipTestStates[rnd.Intn(len(shardOwnershipTestStates))], time.Time{})
		}
	}
	return desc
}

func newShardOwnershipTestRing(t testing.TB, desc *Desc, zoneAware bool) *Ring {
	t.Helper()
	cfg := Config{HeartbeatTimeout: ownershipTestTimeout, ReplicationFactor: 3, ZoneAwarenessEnabled: zoneAware}
	r, err := NewDescFedRing(cfg, testRingName, testRingKey, &MockClient{}, log.NewNopLogger())
	require.NoError(t, err)
	r.UpdateDesc(desc)
	return r
}

// requireShardOwnershipMatchesSubring checks every instance of r, for every
// shard size up to past the instance count, against the subring.
func requireShardOwnershipMatchesSubring(t *testing.T, r *Ring, tenants int) {
	t.Helper()
	ids := make([]string, 0, len(r.ringDesc.Ingesters))
	for id := range r.ringDesc.Ingesters {
		ids = append(ids, id)
	}
	ids = append(ids, "not-in-the-ring")

	for _, op := range []Operation{Write, WriteNoExtend} {
		for tenant := range tenants {
			identifier := fmt.Sprintf("tenant-%d", tenant)
			for size := 0; size <= len(r.ringDesc.Ingesters)+1; size++ {
				sub := r.ShuffleShard(identifier, size).(*Ring)
				want := sub.ZonalOwnership(op)
				for _, id := range ids {
					share, zones, _ := r.InstanceShardZonalOwnership(identifier, size, id, op)
					require.Equal(t, want[id], share, "tenant %s, size %d, instance %s", identifier, size, id)
					require.Equal(t, sub.ZonesCount(), zones, "tenant %s, size %d", identifier, size)
				}
			}
		}
	}
}

func TestRing_InstanceShardZonalOwnership_MatchesSubring(t *testing.T) {
	rnd := rand.New(rand.NewSource(ownershipTestSeed))
	for range 40 {
		requireShardOwnershipMatchesSubring(t, newShardOwnershipTestRing(t, randomShardOwnershipDesc(rnd), true), 5)
	}
}

func TestRing_InstanceShardZonalOwnership_MatchesSubringWithoutZoneAwareness(t *testing.T) {
	rnd := rand.New(rand.NewSource(ownershipTestSeed))
	for range 5 {
		requireShardOwnershipMatchesSubring(t, newShardOwnershipTestRing(t, randomShardOwnershipDesc(rnd), false), 3)
	}
}

func TestRing_InstanceShardZonalOwnership_MatchesSubringWithTokenSharedAcrossZones(t *testing.T) {
	rnd := rand.New(rand.NewSource(ownershipTestSeed))
	for range 10 {
		desc := randomShardOwnershipDesc(rnd)
		// Give an instance of zone-1 a token already held in zone-0.
		var shared uint32
		for _, instance := range desc.Ingesters {
			if instance.Zone == "zone-0" {
				shared = instance.Tokens[0]
				break
			}
		}
		for id, instance := range desc.Ingesters {
			if instance.Zone == "zone-1" {
				instance.Tokens = append(instance.Tokens, shared)
				desc.Ingesters[id] = instance
				break
			}
		}
		requireShardOwnershipMatchesSubring(t, newShardOwnershipTestRing(t, desc, true), 5)
	}
}

func TestRing_InstanceShardZonalOwnership_MatchesSubringWithTokenlessZone(t *testing.T) {
	rnd := rand.New(rand.NewSource(ownershipTestSeed))
	desc := randomShardOwnershipDesc(rnd)
	desc.AddIngester("tokenless", "tokenless", "zone-new", nil, PENDING, time.Time{})
	requireShardOwnershipMatchesSubring(t, newShardOwnershipTestRing(t, desc, true), 5)
}

// shardOwnershipGenerationTestDesc is two zones of three ACTIVE instances.
func shardOwnershipGenerationTestDesc(override func(*Desc)) *Desc {
	desc := NewDesc()
	for z, zone := range []string{"a", "b"} {
		for i := range 3 {
			id := fmt.Sprintf("%s%d", zone, i)
			desc.AddIngester(id, id, zone, []uint32{uint32(z*3+i) * (1 << 28)}, ACTIVE, time.Time{})
		}
	}
	if override != nil {
		override(desc)
	}
	return desc
}

func setState(id string, state InstanceState) func(*Desc) {
	return func(d *Desc) {
		instance := d.Ingesters[id]
		instance.State = state
		d.Ingesters[id] = instance
	}
}

func TestRing_ShardOwnershipGeneration(t *testing.T) {
	tests := map[string]struct {
		from, to func(*Desc)
		op       Operation
		changed  bool
	}{
		"heartbeat only": {
			to: func(d *Desc) {
				for id, instance := range d.Ingesters {
					instance.Timestamp++
					d.Ingesters[id] = instance
				}
			},
			op: Write,
		},
		"state change in another zone": {to: setState("b1", LEAVING), op: Write},
		"READONLY in another zone":     {to: setState("b1", READONLY), op: Write},
		"op extends past it in my zone": {
			to: setState("a1", LEAVING), op: Write, changed: true,
		},
		"op still stops at it in my zone": {
			to: setState("a1", LEAVING), op: WriteNoExtend,
		},
		"op still extends past it in my zone": {
			from: setState("a1", PENDING), to: setState("a1", JOINING), op: Write,
		},
		// The shard pick skips READONLY instances whatever the op does.
		"READONLY changes the pick in my zone": {
			from: setState("a1", LEAVING), to: setState("a1", READONLY), op: Write, changed: true,
		},
		"instance leaves the ring": {
			to: func(d *Desc) { d.RemoveIngester("b2") }, op: Write, changed: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := newShardOwnershipTestRing(t, shardOwnershipGenerationTestDesc(tc.from), true)
			before := r.ShardOwnershipGeneration("a0", tc.op)

			next := shardOwnershipGenerationTestDesc(tc.from)
			tc.to(next)
			r.UpdateDesc(next)

			assert.Equal(t, tc.changed, r.ShardOwnershipGeneration("a0", tc.op) != before)
		})
	}
}

// A cache that recomputes a tenant's share only when the generation changes
// must always hold what a fresh computation returns.
func TestRing_ShardOwnershipGeneration_CacheMatchesFreshComputation(t *testing.T) {
	const (
		steps   = 400
		tenants = 8
	)
	rnd := rand.New(rand.NewSource(ownershipTestSeed))
	desc := randomShardOwnershipDesc(rnd)
	r := newShardOwnershipTestRing(t, desc, true)

	type cached struct {
		share float64
		zones int
		gen   ShardOwnershipGeneration
	}
	type key struct {
		instance, tenant string
		size             int
		op               Operation
	}
	cache := map[key]cached{}
	recomputed := 0

	for step := range steps {
		next := NewDesc()
		for id, instance := range desc.Ingesters {
			instance.Timestamp++
			next.Ingesters[id] = instance
		}
		ids := make([]string, 0, len(next.Ingesters))
		for id := range next.Ingesters {
			ids = append(ids, id)
		}
		switch rnd.Intn(10) {
		case 0: // an instance leaves
			if len(ids) > 2 {
				next.RemoveIngester(ids[rnd.Intn(len(ids))])
			}
		case 1, 2, 3, 4: // a state change
			id := ids[rnd.Intn(len(ids))]
			instance := next.Ingesters[id]
			instance.State = shardOwnershipTestStates[rnd.Intn(len(shardOwnershipTestStates))]
			next.Ingesters[id] = instance
		}
		desc = next
		r.UpdateDesc(desc)

		fresh := newShardOwnershipTestRing(t, desc, true)
		for id := range desc.Ingesters {
			for tenant := range tenants {
				identifier := fmt.Sprintf("tenant-%d", tenant)
				size := 1 + (tenant*7)%(len(desc.Ingesters)+1)
				for _, op := range []Operation{Write, WriteNoExtend} {
					k := key{id, identifier, size, op}
					c, ok := cache[k]
					if !ok || c.gen != r.ShardOwnershipGeneration(id, op) {
						c.share, c.zones, c.gen = r.InstanceShardZonalOwnership(identifier, size, id, op)
						cache[k] = c
						recomputed++
					}
					wantShare, wantZones, _ := fresh.InstanceShardZonalOwnership(identifier, size, id, op)
					require.Equal(t, wantShare, c.share, "step %d, %v", step, k)
					require.Equal(t, wantZones, c.zones, "step %d, %v", step, k)
				}
			}
		}
	}
	t.Logf("recomputed %d times", recomputed)
}
