package ring

import (
	"slices"
	"sort"

	shardUtil "github.com/cortexproject/cortex/pkg/ring/shard"
)

// ringHashSpaceSize is the number of distinct token values in the ring.
const ringHashSpaceSize = float64(maxTokenValue) + 1

// ZonalOwnershipRing is implemented by rings that can report the share of
// the hash space each instance owns within its own zone.
type ZonalOwnershipRing interface {
	// ZonalOwnership returns, for every instance in the ring, the fraction
	// of its zone's hash space it owns for writes made with op. See
	// (*Ring).ZonalOwnership for the exact semantics.
	ZonalOwnership(op Operation) map[string]float64

	// ZonesCount returns the number of zones with at least one instance with
	// tokens. Only when it equals the replication factor does each zone hold
	// exactly one copy of every key, which is what ZonalOwnership assumes.
	ZonesCount() int
}

// ZonesCount returns the number of zones with at least one instance with tokens.
func (r *Ring) ZonesCount() int {
	r.mtx.RLock()
	defer r.mtx.RUnlock()
	return len(r.ringZones)
}

// ZonalOwnership returns, for every instance in the ring, the fraction of its
// zone's hash space that op routes to that instance.
//
// Tokens of instances for which op extends the replica set (for example
// JOINING or LEAVING instances with Write, or READONLY instances with either
// write operation) are left out: their ranges fall to the next remaining token
// in the same zone, which is the instance Get extends to. Instances excluded
// this way report 0. Instances that Get keeps but Filter drops without
// replacement (such as an ACTIVE instance with a stale heartbeat) keep their
// tokens, because no other instance in the zone receives those keys.
//
// The values are the fraction of keys each instance receives only with
// zone-aware replication and a replication factor equal to ZonesCount. With
// more replicas than zones, Get puts a second copy in some zones, and with
// fewer it skips some zones, so callers should not use the values then.
//
// The returned map is cached until the ring's tokens or instance states
// change, and must not be modified.
func (r *Ring) ZonalOwnership(op Operation) map[string]float64 {
	r.mtx.RLock()
	cached, ok := r.zonalOwnershipByOp[op]
	r.mtx.RUnlock()
	if ok {
		return cached
	}

	r.mtx.Lock()
	defer r.mtx.Unlock()
	if cached, ok := r.zonalOwnershipByOp[op]; ok {
		return cached
	}

	ownership := r.computeZonalOwnership(op)
	if r.zonalOwnershipByOp == nil {
		r.zonalOwnershipByOp = map[Operation]map[string]float64{}
	}
	r.zonalOwnershipByOp[op] = ownership
	return ownership
}

// invalidateZonalOwnership drops the cached ownership. The caller must hold
// r.mtx for writing.
func (r *Ring) invalidateZonalOwnership() {
	r.zonalOwnershipByOp = nil
}

// invalidateZonalOwnershipOnStateChange drops the ownership cached for each
// operation that extends the replica set past exactly one of the two states:
// only those state changes move ranges. The caller must hold r.mtx for writing.
func (r *Ring) invalidateZonalOwnershipOnStateChange(prev, next InstanceState) {
	if prev == next {
		return
	}
	for op := range r.zonalOwnershipByOp {
		if op.ShouldExtendReplicaSetOnState(prev) != op.ShouldExtendReplicaSetOnState(next) {
			delete(r.zonalOwnershipByOp, op)
		}
	}
}

// computeZonalOwnership must be called with r.mtx held.
func (r *Ring) computeZonalOwnership(op Operation) map[string]float64 {
	ownership := make(map[string]float64, len(r.ringDesc.Ingesters))
	for id := range r.ringDesc.Ingesters {
		ownership[id] = 0
	}

	for _, zoneTokens := range r.ringTokensByZone {
		r.zoneOwnership(zoneTokens, op, func(instanceID string, share float64) {
			ownership[instanceID] += share
		})
	}

	return ownership
}

// zoneOwnership calls add with the share of the hash space each of a zone's
// sorted tokens gives the instance holding it, leaving out the tokens of
// instances op extends the replica set past. The caller must hold r.mtx.
func (r *Ring) zoneOwnership(zoneTokens []uint32, op Operation, add func(instanceID string, share float64)) {
	// Tokens are sorted, so the remaining tokens stay sorted.
	remaining := make([]uint32, 0, len(zoneTokens))
	for _, token := range zoneTokens {
		if r.ownsTokenForWrites(token, op) {
			remaining = append(remaining, token)
		}
	}

	for i, token := range remaining {
		prev := remaining[(i+len(remaining)-1)%len(remaining)]
		add(r.ringInstanceByToken[token].InstanceID, float64(ownedRange(prev, token, len(remaining)))/ringHashSpaceSize)
	}
}

// ShardOwnershipGeneration identifies a version of the ring as far as one
// instance's shard ownership is concerned. It changes on any topology change,
// and on a state change in the instance's zone that can move a shard pick or a
// range for the operation it was asked for.
type ShardOwnershipGeneration struct {
	topology, zone uint64
}

// ShardOwnershipGeneration returns the current generation for the zone of
// instanceID and op. A result of InstanceShardZonalOwnership stays valid while
// it is unchanged.
func (r *Ring) ShardOwnershipGeneration(instanceID string, op Operation) ShardOwnershipGeneration {
	r.registerOwnershipOp(op)

	r.mtx.RLock()
	defer r.mtx.RUnlock()
	return ShardOwnershipGeneration{
		topology: r.ownershipTopologyGeneration,
		zone:     r.ownershipZoneGenerations[r.ringDesc.Ingesters[instanceID].Zone],
	}
}

// InstanceShardZonalOwnership returns what
// ShuffleShard(identifier, size).ZonalOwnership(op)[instanceID] and the
// subring's ZonesCount return, without building the subring: both the shard
// pick and ownership are per zone, so only instanceID's zone is read. The
// generation is read before the results are computed, so it never claims a
// result is newer than it is.
func (r *Ring) InstanceShardZonalOwnership(identifier string, size int, instanceID string, op Operation) (share float64, zones int, gen ShardOwnershipGeneration) {
	gen = r.ShardOwnershipGeneration(instanceID, op)

	if size <= 0 || r.InstancesCount() <= size {
		return r.ZonalOwnership(op)[instanceID], r.ZonesCount(), gen
	}

	if share, zones, ok := r.instanceShardZonalOwnership(identifier, size, instanceID, op); ok {
		return share, zones, gen
	}

	// Without zone awareness, or with a token shared across zones, one zone's
	// pick and ownership can depend on others: build the subring.
	sub := r.ShuffleShard(identifier, size).(*Ring)
	return sub.ZonalOwnership(op)[instanceID], sub.ZonesCount(), gen
}

// instanceShardZonalOwnership computes InstanceShardZonalOwnership from
// instanceID's zone alone. It reports false when that is not exact.
func (r *Ring) instanceShardZonalOwnership(identifier string, size int, instanceID string, op Operation) (float64, int, bool) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()

	if !r.cfg.ZoneAwarenessEnabled || r.crossZoneTokens {
		return 0, 0, false
	}

	// Every zone with tokens gets at least one shard member.
	zones := 0
	for _, zone := range r.ringZones {
		if len(r.ringTokensByZone[zone]) > 0 {
			zones++
		}
	}

	instance, ok := r.ringDesc.Ingesters[instanceID]
	if !ok {
		return 0, zones, true
	}
	zone := instance.Zone

	shard := map[string]InstanceDesc{}
	count := shardUtil.ShuffleShardExpectedInstancesPerZone(size, len(r.ringZones))
	r.pickShardInstancesInZone(identifier, zone, r.ringTokensByZone[zone], count, shard, func(member InstanceDesc) bool {
		return member.State == READONLY
	})
	if _, ok := shard[instanceID]; !ok {
		return 0, zones, true
	}

	memberTokens := make([][]uint32, 0, len(shard))
	for _, member := range shard {
		tokens := member.Tokens
		if !sort.IsSorted(Tokens(tokens)) {
			tokens = slices.Sorted(slices.Values(tokens))
		}
		memberTokens = append(memberTokens, tokens)
	}
	share := 0.0
	r.zoneOwnership(MergeTokens(memberTokens), op, func(id string, s float64) {
		if id == instanceID {
			share += s
		}
	})
	return share, zones, true
}

// hasCrossZoneTokens reports whether a token listed under one zone maps to an
// instance of another zone.
func hasCrossZoneTokens(tokensByZone map[string][]uint32, instanceByToken map[uint32]instanceInfo) bool {
	for zone, tokens := range tokensByZone {
		for _, token := range tokens {
			if instanceByToken[token].Zone != zone {
				return true
			}
		}
	}
	return false
}

// registerOwnershipOp makes state changes bump zone generations for op.
func (r *Ring) registerOwnershipOp(op Operation) {
	r.mtx.RLock()
	_, ok := r.ownershipOps[op]
	r.mtx.RUnlock()
	if ok {
		return
	}

	r.mtx.Lock()
	defer r.mtx.Unlock()
	if r.ownershipOps == nil {
		r.ownershipOps = map[Operation]struct{}{}
	}
	r.ownershipOps[op] = struct{}{}
}

// bumpOwnershipZoneGeneration records a state change of an instance in zone if
// it can move a shard pick (READONLY instances are skipped when picking) or a
// range for a registered operation. The caller must hold r.mtx for writing.
func (r *Ring) bumpOwnershipZoneGeneration(zone string, prev, next InstanceState) {
	if prev == next {
		return
	}
	changed := (prev == READONLY) != (next == READONLY)
	for op := range r.ownershipOps {
		changed = changed || op.ShouldExtendReplicaSetOnState(prev) != op.ShouldExtendReplicaSetOnState(next)
	}
	if !changed {
		return
	}
	if r.ownershipZoneGenerations == nil {
		r.ownershipZoneGenerations = map[string]uint64{}
	}
	r.ownershipZoneGenerations[zone]++
}

// ownsTokenForWrites reports whether writes made with op stop at the instance
// holding token, rather than extending past it.
func (r *Ring) ownsTokenForWrites(token uint32, op Operation) bool {
	info, ok := r.ringInstanceByToken[token]
	if !ok {
		return false
	}
	instance, ok := r.ringDesc.Ingesters[info.InstanceID]
	if !ok {
		return false
	}
	return !op.ShouldExtendReplicaSetOnState(instance.State)
}

// ownedRange returns the number of token values in (prev, token], the range
// owned by token when prev is the preceding token in the same zone.
func ownedRange(prev, token uint32, zoneTokenCount int) int64 {
	if prev == token && zoneTokenCount > 1 {
		// A duplicate token: the first copy already owns the range.
		return 0
	}
	return tokenDistance(prev, token)
}
