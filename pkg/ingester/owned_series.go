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

const (
	// ownedStripeBits stripes the tracker by the token's top bits, so hooks
	// on different stripes don't contend.
	ownedStripeBits  = 9
	ownedStripeCount = 1 << ownedStripeBits
	ownedStripeShift = 32 - ownedStripeBits
)

// ownedSeriesTracker tracks the tokens of a tenant's head series, to count
// those in the ranges this ingester owns.
type ownedSeriesTracker struct {
	stripes [ownedStripeCount]ownedSeriesStripe

	// rangesMtx serializes setRanges and clearRanges.
	rangesMtx  sync.Mutex
	known      bool
	intervals  []tokenInterval
	rangesHash uint64

	// Sum of the stripes' notOwned; read lock-free by PreCreation.
	notOwned atomic.Int64
}

type ownedSeriesStripe struct {
	mtx    sync.Mutex
	tokens *roaring.Bitmap
	// Series beyond the first with the same token.
	extra map[uint32]uint32
	total int64

	// Shared, read-only owned intervals; only meaningful when known.
	known     bool
	intervals []tokenInterval
	notOwned  int64
}

func newOwnedSeriesTracker() *ownedSeriesTracker {
	t := &ownedSeriesTracker{}
	for i := range t.stripes {
		t.stripes[i].tokens = roaring.New()
		t.stripes[i].extra = map[uint32]uint32{}
	}
	return t
}

func (t *ownedSeriesTracker) stripe(token uint32) *ownedSeriesStripe {
	return &t.stripes[token>>ownedStripeShift]
}

func (t *ownedSeriesTracker) add(token uint32) {
	s := t.stripe(token)
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if !s.tokens.CheckedAdd(token) {
		s.extra[token]++
	}
	s.total++
	if s.known && !ownsToken(s.intervals, token) {
		s.notOwned++
		t.notOwned.Inc()
	}
}

func (t *ownedSeriesTracker) remove(token uint32) {
	s := t.stripe(token)
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if c, ok := s.extra[token]; ok {
		if c == 1 {
			delete(s.extra, token)
		} else {
			s.extra[token] = c - 1
		}
	} else if !s.tokens.CheckedRemove(token) {
		return // Not tracked.
	}
	s.total--
	if s.known && !ownsToken(s.intervals, token) {
		s.notOwned--
		t.notOwned.Dec()
	}
}

// setRanges recounts the owned series if the ranges changed.
func (t *ownedSeriesTracker) setRanges(ranges []ring.TokenRange) {
	intervals := toIntervals(ranges)
	hash := hashIntervals(intervals)

	t.rangesMtx.Lock()
	defer t.rangesMtx.Unlock()
	if t.known && t.rangesHash == hash {
		return
	}
	t.known, t.intervals, t.rangesHash = true, intervals, hash

	// Built outside the stripe locks; intersecting with run containers beats
	// one Rank per bound, which walks containers.
	ownedTokens := roaring.New()
	for _, iv := range intervals {
		ownedTokens.AddRange(iv.start, iv.end)
	}
	for i := range t.stripes {
		s := &t.stripes[i]
		s.mtx.Lock()
		owned := int64(s.tokens.AndCardinality(ownedTokens))
		for token, c := range s.extra {
			if ownsToken(intervals, token) {
				owned += int64(c)
			}
		}
		notOwned := s.total - owned
		t.notOwned.Add(notOwned - s.notOwned)
		s.known, s.intervals, s.notOwned = true, intervals, notOwned
		s.mtx.Unlock()
	}
}

// clearRanges falls back to counting every tracked series as owned.
func (t *ownedSeriesTracker) clearRanges() {
	t.rangesMtx.Lock()
	defer t.rangesMtx.Unlock()
	t.known, t.intervals, t.rangesHash = false, nil, 0
	for i := range t.stripes {
		s := &t.stripes[i]
		s.mtx.Lock()
		t.notOwned.Sub(s.notOwned)
		s.known, s.intervals, s.notOwned = false, nil, 0
		s.mtx.Unlock()
	}
}

// totals returns tracked and not-owned series, consistent per stripe.
func (t *ownedSeriesTracker) totals() (total, notOwned int64) {
	for i := range t.stripes {
		s := &t.stripes[i]
		s.mtx.Lock()
		total += s.total
		notOwned += s.notOwned
		s.mtx.Unlock()
	}
	return total, notOwned
}

// owned returns the number of tracked series in the owned ranges.
func (t *ownedSeriesTracker) owned() int64 {
	total, notOwned := t.totals()
	return total - notOwned
}

// ownsToken reports whether token is in the sorted intervals.
func ownsToken(intervals []tokenInterval, token uint32) bool {
	k := uint64(token)
	i := sort.Search(len(intervals), func(i int) bool { return intervals[i].end > k })
	return i < len(intervals) && intervals[i].start <= k
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
