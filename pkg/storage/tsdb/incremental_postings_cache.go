package tsdb

import (
	"context"
	"sort"
	"sync"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/index"
)

// incrementalHeadPostingsCache caches head block postings results and extends them
// incrementally when new series are created. Since series IDs are monotonically
// increasing, cached results are always a valid subset of the current truth.
//
// On cache hit, only series created after the cached maxID are checked against
// the matchers (via LabelValueFor per new series). This is fast because the
// delta is typically tiny (10-100 series between queries) compared to a full
// regex scan (150K+ label values).
type incrementalHeadPostingsCache struct {
	mu      sync.RWMutex
	entries map[string]*incrementalEntry

	postingsForMatchersFunc func(ctx context.Context, ix tsdb.IndexReader, ms ...*labels.Matcher) (index.Postings, error)
}

type incrementalEntry struct {
	mu    sync.Mutex
	refs  []storage.SeriesRef
	maxID storage.SeriesRef
}

func newIncrementalHeadPostingsCache(postingsForMatchersFunc func(ctx context.Context, ix tsdb.IndexReader, ms ...*labels.Matcher) (index.Postings, error)) *incrementalHeadPostingsCache {
	if postingsForMatchersFunc == nil {
		postingsForMatchersFunc = tsdb.PostingsForMatchers
	}
	return &incrementalHeadPostingsCache{
		entries:                 make(map[string]*incrementalEntry),
		postingsForMatchersFunc: postingsForMatchersFunc,
	}
}

func (c *incrementalHeadPostingsCache) PostingsForMatchers(ctx context.Context, ix tsdb.IndexReader, ms ...*labels.Matcher) ([]storage.SeriesRef, error) {
	key := matchersKeyIncremental(ms)

	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		return c.coldMiss(ctx, ix, ms, key)
	}

	return c.extend(ctx, ix, ms, entry)
}

func (c *incrementalHeadPostingsCache) coldMiss(ctx context.Context, ix tsdb.IndexReader, ms []*labels.Matcher, key string) ([]storage.SeriesRef, error) {
	postings, err := c.postingsForMatchersFunc(ctx, ix, ms...)
	if err != nil {
		return nil, err
	}

	ids, err := index.ExpandPostings(postings)
	if err != nil {
		return nil, err
	}

	// Find the current max series ID for this metric
	maxID := maxIDForMetric(ctx, ix, ms)

	entry := &incrementalEntry{
		refs:  ids,
		maxID: maxID,
	}

	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()

	return ids, nil
}

func (c *incrementalHeadPostingsCache) extend(ctx context.Context, ix tsdb.IndexReader, ms []*labels.Matcher, entry *incrementalEntry) ([]storage.SeriesRef, error) {
	entry.mu.Lock()
	defer entry.mu.Unlock()

	// Split matchers into equality (can use Postings directly) and regex (need LabelValueFor)
	var equalityNames []string
	var equalityValues []string
	var regexMs []*labels.Matcher
	for _, m := range ms {
		if m.Type == labels.MatchEqual {
			equalityNames = append(equalityNames, m.Name)
			equalityValues = append(equalityValues, m.Value)
		} else {
			regexMs = append(regexMs, m)
		}
	}

	if len(equalityNames) == 0 {
		return entry.refs, nil
	}

	// Get postings for each equality matcher, seek past maxID, intersect
	// Start with the first equality matcher's postings
	p, err := ix.Postings(ctx, equalityNames[0], equalityValues[0])
	if err != nil {
		return entry.refs, nil
	}
	if !p.Seek(entry.maxID + 1) {
		// No new series for first matcher
		entry.maxID = maxIDForMetric(ctx, ix, ms)
		return entry.refs, nil
	}

	// Collect refs from first matcher after maxID
	var deltaIDs []storage.SeriesRef
	for {
		deltaIDs = append(deltaIDs, p.At())
		if !p.Next() {
			break
		}
	}

	// If delta is too large, fall back to full recomputation
	// (LabelValueFor per-series is expensive; beyond ~1000 series the full
	// regex scan over all label values is cheaper)
	if len(deltaIDs) > 1000 {
		postings, err := c.postingsForMatchersFunc(ctx, ix, ms...)
		if err != nil {
			return entry.refs, nil
		}
		ids, err := index.ExpandPostings(postings)
		if err != nil {
			return entry.refs, nil
		}
		entry.refs = ids
		if len(deltaIDs) > 0 {
			entry.maxID = deltaIDs[len(deltaIDs)-1]
		}
		return entry.refs, nil
	}

	// Intersect with remaining equality matchers' postings (also after maxID)
	for i := 1; i < len(equalityNames) && len(deltaIDs) > 0; i++ {
		p, err := ix.Postings(ctx, equalityNames[i], equalityValues[i])
		if err != nil {
			return entry.refs, nil
		}
		if !p.Seek(entry.maxID + 1) {
			deltaIDs = deltaIDs[:0]
			break
		}
		var otherIDs []storage.SeriesRef
		for {
			otherIDs = append(otherIDs, p.At())
			if !p.Next() {
				break
			}
		}
		deltaIDs = intersectSortedRefs(deltaIDs, otherIDs)
	}

	if len(deltaIDs) == 0 {
		return entry.refs, nil
	}

	// Now check regex matchers only on the small intersected set
	var newRefs []storage.SeriesRef
	for _, ref := range deltaIDs {
		matches := true
		for _, m := range regexMs {
			val, err := ix.LabelValueFor(ctx, ref, m.Name)
			if err != nil {
				val = ""
			}
			if !m.Matches(val) {
				matches = false
				break
			}
		}
		if matches {
			newRefs = append(newRefs, ref)
		}
	}

	if len(newRefs) > 0 {
		entry.refs = append(entry.refs, newRefs...)
	}

	// Update maxID from the delta
	newMax := entry.maxID
	if len(deltaIDs) > 0 && deltaIDs[len(deltaIDs)-1] > newMax {
		newMax = deltaIDs[len(deltaIDs)-1]
	}
	entry.maxID = newMax

	return entry.refs, nil
}

func intersectSortedRefs(a, b []storage.SeriesRef) []storage.SeriesRef {
	result := a[:0]
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			result = append(result, a[i])
			i++
			j++
		} else if a[i] < b[j] {
			i++
		} else {
			j++
		}
	}
	return result
}

func (c *incrementalHeadPostingsCache) Clear() {
	c.mu.Lock()
	c.entries = make(map[string]*incrementalEntry)
	c.mu.Unlock()
}

func maxIDForMetric(ctx context.Context, ix tsdb.IndexReader, ms []*labels.Matcher) storage.SeriesRef {
	metricName := metricNameFromMatchers(ms)
	if metricName == "" {
		return 0
	}
	p, err := ix.Postings(ctx, labels.MetricName, metricName)
	if err != nil {
		return 0
	}
	// For ListPostings (head block), we can get length and seek efficiently.
	// Seek to max possible value — the last At() before Seek returns false
	// is the max. But Seek past end returns false with At()=0.
	// Instead, just iterate to the end — but that's O(N)...
	// Better: use the fact that postings are sorted. If we know the length,
	// we could index directly. But we can't without expanding.
	// Compromise: just use the last deltaID as maxID when available.
	// This function is only needed on cold miss. On extend, we use deltaIDs.
	var maxID storage.SeriesRef
	for p.Next() {
		maxID = p.At()
	}
	return maxID
}

func metricNameFromMatchers(ms []*labels.Matcher) string {
	for _, m := range ms {
		if m.Name == labels.MetricName && m.Type == labels.MatchEqual {
			return m.Value
		}
	}
	return ""
}

func matchersKeyIncremental(ms []*labels.Matcher) string {
	sorted := make([]*labels.Matcher, len(ms))
	copy(sorted, ms)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Type != sorted[j].Type {
			return sorted[i].Type < sorted[j].Type
		}
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Value < sorted[j].Value
	})
	var key string
	for _, m := range sorted {
		key += m.String() + "\x00"
	}
	return key
}
