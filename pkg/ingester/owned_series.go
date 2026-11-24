package ingester

import (
	"encoding/binary"
	"slices"
	"sort"
	"sync"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/cespare/xxhash/v2"
	"go.uber.org/atomic"

	"github.com/cortexproject/cortex/pkg/ring"
)

// tokenSpaceEnd is one past the largest token.
const tokenSpaceEnd = uint64(1) << 32

// tokenInterval is the tokens in [start, end), with end <= tokenSpaceEnd.
type tokenInterval struct {
	start, end uint64
}

// ownedSeriesTracker tracks the tokens of a tenant's head series, to count
// those in the ranges this ingester owns.
type ownedSeriesTracker struct {
	mtx    sync.Mutex
	tokens *roaring.Bitmap
	// Series beyond the first with the same token.
	extra map[uint32]uint16
	total int64

	// Sorted, disjoint owned intervals; only meaningful when known.
	known      bool
	intervals  []tokenInterval
	rangesHash uint64

	// Tracked series outside the owned intervals; 0 while not known.
	notOwned atomic.Int64
}

func newOwnedSeriesTracker() *ownedSeriesTracker {
	return &ownedSeriesTracker{tokens: roaring.New(), extra: map[uint32]uint16{}}
}

func (t *ownedSeriesTracker) add(token uint32) {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	if !t.tokens.CheckedAdd(token) {
		t.extra[token]++
	}
	t.total++
	if t.known && !t.ownsLocked(token) {
		t.notOwned.Inc()
	}
}

func (t *ownedSeriesTracker) remove(token uint32) {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	if c, ok := t.extra[token]; ok {
		if c == 1 {
			delete(t.extra, token)
		} else {
			t.extra[token] = c - 1
		}
	} else if !t.tokens.CheckedRemove(token) {
		return // Not tracked.
	}
	t.total--
	if t.known && !t.ownsLocked(token) {
		t.notOwned.Dec()
	}
}

// setRanges recounts the owned series if the ranges changed.
func (t *ownedSeriesTracker) setRanges(ranges []ring.TokenRange) {
	intervals := toIntervals(ranges)
	hash := hashIntervals(intervals)

	t.mtx.Lock()
	defer t.mtx.Unlock()
	if t.known && t.rangesHash == hash {
		return
	}
	t.known, t.intervals, t.rangesHash = true, intervals, hash

	// Intersecting with run containers beats one Rank per bound, which walks containers.
	ownedTokens := roaring.New()
	for _, iv := range intervals {
		ownedTokens.AddRange(iv.start, iv.end)
	}
	owned := int64(t.tokens.AndCardinality(ownedTokens))
	for token, c := range t.extra {
		if t.ownsLocked(token) {
			owned += int64(c)
		}
	}
	t.notOwned.Store(t.total - owned)
}

// clearRanges falls back to counting every tracked series as owned.
func (t *ownedSeriesTracker) clearRanges() {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	t.known, t.intervals, t.rangesHash = false, nil, 0
	t.notOwned.Store(0)
}

// owned returns the number of tracked series in the owned ranges.
func (t *ownedSeriesTracker) owned() int64 {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	return t.total - t.notOwned.Load()
}

// ownsLocked reports whether token is in the owned intervals. Caller holds t.mtx.
func (t *ownedSeriesTracker) ownsLocked(token uint32) bool {
	k := uint64(token)
	i := sort.Search(len(t.intervals), func(i int) bool { return t.intervals[i].end > k })
	return i < len(t.intervals) && t.intervals[i].start <= k
}

// toIntervals splits wrapping ranges and merges adjacent ones.
func toIntervals(ranges []ring.TokenRange) []tokenInterval {
	out := make([]tokenInterval, 0, len(ranges)+1)
	for _, r := range ranges {
		s, e := uint64(r.Start), uint64(r.End)
		if s < e {
			out = append(out, tokenInterval{s, e})
			continue
		}
		out = append(out, tokenInterval{s, tokenSpaceEnd})
		if e > 0 {
			out = append(out, tokenInterval{0, e})
		}
	}
	slices.SortFunc(out, func(a, b tokenInterval) int {
		switch {
		case a.start < b.start:
			return -1
		case a.start > b.start:
			return 1
		}
		return 0
	})
	merged := out[:0]
	for _, iv := range out {
		if n := len(merged); n > 0 && iv.start <= merged[n-1].end {
			merged[n-1].end = max(merged[n-1].end, iv.end)
			continue
		}
		merged = append(merged, iv)
	}
	return merged
}

func hashIntervals(intervals []tokenInterval) uint64 {
	b := make([]byte, 0, len(intervals)*16)
	for _, iv := range intervals {
		b = binary.LittleEndian.AppendUint64(b, iv.start)
		b = binary.LittleEndian.AppendUint64(b, iv.end)
	}
	return xxhash.Sum64(b)
}

func (i *Ingester) ownedSeriesTrackingEnabled() bool {
	return i.cfg.OwnedSeriesLimitEnabled && i.cfg.UseRingOwnershipForLimits && i.cfg.DistributorShardByAllLabels
}

// setOwnedRanges updates the tenant's tracker and returns its owned series.
func (i *Ingester) setOwnedRanges(userID string, ranges []ring.TokenRange, ok bool) (int64, bool) {
	db, err := i.getTSDB(userID)
	if err != nil || db.ownedSeries == nil {
		return 0, false
	}
	if ok {
		db.ownedSeries.setRanges(ranges)
	} else {
		db.ownedSeries.clearRanges()
	}
	return db.ownedSeries.owned(), true
}
