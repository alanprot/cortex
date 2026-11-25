package ring

// ringHashSpaceSize is the number of distinct token values in the ring.
const ringHashSpaceSize = float64(maxTokenValue) + 1

// ZonalOwnershipRing is implemented by rings that can report the share of
// the hash space each instance owns within its own zone.
type ZonalOwnershipRing interface {
	// ZonalOwnership returns, for every instance in the ring, the fraction
	// of its zone's hash space it owns. See (*Ring).ZonalOwnership for the
	// exact semantics.
	ZonalOwnership() map[string]float64

	// ZonesCount returns the number of zones with at least one instance with
	// tokens. Only when it equals the replication factor does each zone hold
	// exactly one copy of every key, which is what ZonalOwnership assumes.
	ZonesCount() int

	// ZonalOwnedRanges returns the token ranges whose keys are written to
	// instanceID within its zone. See (*Ring).ZonalOwnedRanges.
	ZonalOwnedRanges(instanceID string) []TokenRange
}

// TokenRange is the keys in [Start, End), wrapping around the end of the hash
// space when End <= Start. Get routes a key to the first token above it.
type TokenRange struct {
	Start, End uint32
}

// ZonesCount returns the number of zones with at least one instance with tokens.
func (r *Ring) ZonesCount() int {
	r.mtx.RLock()
	defer r.mtx.RUnlock()
	return len(r.ringZones)
}

// HasDuplicateTokens reports whether two instances of the ring hold the same
// token. A token maps to one instance ring-wide, so when the two are in
// different zones, one zone's ownership and shuffle-shard picks depend on an
// instance of another zone.
func (r *Ring) HasDuplicateTokens() bool {
	r.mtx.RLock()
	defer r.mtx.RUnlock()
	return len(r.ringTokens) != len(r.ringInstanceByToken)
}

// ZonalOwnership returns, for every instance in the ring, the fraction of its
// zone's hash space it owns.
//
// Every instance owns the ranges of its tokens, except READONLY instances:
// writes skip them, and their ranges fall to the next instance in the same
// zone, which is the instance Get extends to. READONLY instances report 0.
// A token shared across zones counts only in the zone of the instance the
// ring's token map holds for it, which is where Get sends its keys.
// This is where writes go with -distributor.extend-writes=false. With it on,
// writes also skip JOINING, LEAVING and PENDING instances, which this does not
// model.
//
// The values are the fraction of keys each instance receives only with
// zone-aware replication and a replication factor equal to ZonesCount. With
// more replicas than zones, Get puts a second copy in some zones, and with
// fewer it skips some zones, so callers should not use the values then.
//
// The returned map is cached until the ring's tokens change or an instance
// becomes or stops being READONLY, and must not be modified.
func (r *Ring) ZonalOwnership() map[string]float64 {
	ownership, _ := r.zonalOwnershipAndRanges()
	return ownership
}

// ZonalOwnedRanges returns the [prev, token) ranges instanceID owns within its
// zone, by the same rules as ZonalOwnership. Cached with it; do not modify.
func (r *Ring) ZonalOwnedRanges(instanceID string) []TokenRange {
	_, ranges := r.zonalOwnershipAndRanges()
	return ranges[instanceID]
}

// zonalOwnershipAndRanges computes shares and ranges in one walk, cached until
// the ring's tokens or READONLY states change.
func (r *Ring) zonalOwnershipAndRanges() (map[string]float64, map[string][]TokenRange) {
	r.mtx.RLock()
	ownership, ranges := r.zonalOwnership, r.zonalOwnedRanges
	r.mtx.RUnlock()
	if ownership != nil {
		return ownership, ranges
	}

	r.mtx.Lock()
	defer r.mtx.Unlock()
	if r.zonalOwnership == nil {
		r.zonalOwnership, r.zonalOwnedRanges = r.computeZonalOwnership()
	}
	return r.zonalOwnership, r.zonalOwnedRanges
}

// invalidateZonalOwnership drops the cached ownership. The caller must hold
// r.mtx for writing.
func (r *Ring) invalidateZonalOwnership() {
	r.zonalOwnership, r.zonalOwnedRanges = nil, nil
}

// computeZonalOwnership must be called with r.mtx held.
func (r *Ring) computeZonalOwnership() (map[string]float64, map[string][]TokenRange) {
	ownership := make(map[string]float64, len(r.ringDesc.Ingesters))
	ranges := make(map[string][]TokenRange, len(r.ringDesc.Ingesters))
	for id := range r.ringDesc.Ingesters {
		ownership[id] = 0
	}
	r.walkZonalOwnership(func(id string, prev, token uint32, size int64) {
		ownership[id] += float64(size) / ringHashSpaceSize
		ranges[id] = append(ranges[id], TokenRange{Start: prev, End: token})
	})
	return ownership, ranges
}

// walkZonalOwnership calls fn for every non-empty range an instance owns
// within its zone: the keys in [prev, token). It must be called with r.mtx held.
func (r *Ring) walkZonalOwnership(fn func(id string, prev, token uint32, size int64)) {
	for zone, zoneTokens := range r.ringTokensByZone {
		// Tokens are sorted, so the remaining tokens stay sorted.
		remaining := make([]uint32, 0, len(zoneTokens))
		for _, token := range zoneTokens {
			// Get routes a token shared across zones to the instance the token
			// map holds; another zone's copy leaves its range to the next token.
			if r.ownsToken(token) && r.ringInstanceByToken[token].Zone == zone {
				remaining = append(remaining, token)
			}
		}

		for i, token := range remaining {
			prev := remaining[(i+len(remaining)-1)%len(remaining)]
			if size := ownedRange(prev, token, len(remaining)); size > 0 {
				fn(r.ringInstanceByToken[token].InstanceID, prev, token, size)
			}
		}
	}
}

// ownsToken reports whether the instance holding token owns its range, that
// is, whether it is not READONLY.
func (r *Ring) ownsToken(token uint32) bool {
	info, ok := r.ringInstanceByToken[token]
	if !ok {
		return false
	}
	instance, ok := r.ringDesc.Ingesters[info.InstanceID]
	return ok && instance.State != READONLY
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
