package tsdb

import (
	"context"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/index"
)

// newCachedPostingsForMatchersFunc returns a PostingsForMatchers function that
// caches regex evaluation results on label values. It delegates to the standard
// PostingsForMatchers but replaces the regex scan with a cached lookup.
func newCachedPostingsForMatchersFunc(cache *regexMatchCache) func(ctx context.Context, ix tsdb.IndexReader, ms ...*labels.Matcher) (index.Postings, error) {
	return func(ctx context.Context, ix tsdb.IndexReader, ms ...*labels.Matcher) (index.Postings, error) {
		wrapped := &matcherCachedIndexReader{
			IndexReader: ix,
			cache:       cache,
			matchers:    ms,
		}
		return tsdb.PostingsForMatchers(ctx, wrapped, ms...)
	}
}

// matcherCachedIndexReader wraps IndexReader. When PostingsForLabelMatching is
// called, it checks if any of the original matchers match the label name being
// queried, and if so uses the regex cache instead of scanning all values.
type matcherCachedIndexReader struct {
	tsdb.IndexReader
	cache    *regexMatchCache
	matchers []*labels.Matcher
}

func (r *matcherCachedIndexReader) PostingsForLabelMatching(ctx context.Context, name string, match func(string) bool) index.Postings {
	// Find the matcher for this label name to get the pattern string for caching
	var matcher *labels.Matcher
	for _, m := range r.matchers {
		if m.Name == name && (m.Type == labels.MatchRegexp || m.Type == labels.MatchNotRegexp) {
			matcher = m
			break
		}
	}

	if matcher == nil {
		return r.IndexReader.PostingsForLabelMatching(ctx, name, match)
	}

	// Fast check: how many values does this label have?
	count := r.IndexReader.LabelValuesCount(name)
	if count == 0 {
		return index.EmptyPostings()
	}

	// Check if cache already has all values checked
	key := name + "\x00" + matcher.Value
	r.cache.mu.RLock()
	entry, ok := r.cache.entries[key]
	r.cache.mu.RUnlock()

	if ok && entry.checkedCount == count {
		// Pure cache hit — no need to call LabelValues at all
		entry.mu.Lock()
		vals := entry.matchingValues
		entry.mu.Unlock()
		if len(vals) == 0 {
			return index.EmptyPostings()
		}
		p, err := r.IndexReader.Postings(ctx, name, vals...)
		if err != nil {
			return index.EmptyPostings()
		}
		return p
	}

	// Cache miss or new values — need to get the full values slice
	allValues, err := r.IndexReader.LabelValues(ctx, name, (*storage.LabelHints)(nil))
	if err != nil || len(allValues) == 0 {
		return index.EmptyPostings()
	}

	matchingValues := r.cache.GetMatchingValues(allValues, name, matcher.Value, match)
	if len(matchingValues) == 0 {
		return index.EmptyPostings()
	}

	p, err := r.IndexReader.Postings(ctx, name, matchingValues...)
	if err != nil {
		return index.EmptyPostings()
	}
	return p
}

// PostingsForAllLabelValues needs to pass through unchanged
func (r *matcherCachedIndexReader) PostingsForAllLabelValues(ctx context.Context, name string) index.Postings {
	return r.IndexReader.PostingsForAllLabelValues(ctx, name)
}

// LabelValues must pass through to get the real values for the cache
func (r *matcherCachedIndexReader) LabelValues(ctx context.Context, name string, hints *storage.LabelHints, matchers ...*labels.Matcher) ([]string, error) {
	return r.IndexReader.LabelValues(ctx, name, hints, matchers...)
}
