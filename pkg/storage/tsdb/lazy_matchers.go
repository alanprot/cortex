package tsdb

import (
	"context"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	prom_tsdb "github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/index"
)

// splitMatchersForHead separates matchers into those used for postings lookup and those
// applied lazily during iteration. For the head block, regex matchers on high-cardinality
// labels are deferred to avoid expensive regex scans on cache miss, since the postings
// cache is frequently invalidated by new series.
//
// A matcher is deferred only when:
//   - The query already contains a __name__ equality matcher (to ensure selectivity)
//   - The matcher is a regex or negative regex on a non-__name__ label
//   - The label's cardinality exceeds the configured threshold
//   - The estimated series count from the selective matchers is smaller than the regex
//     label's cardinality, ensuring the lazy iteration is cheaper than the full scan
func splitMatchersForHead(ctx context.Context, ix prom_tsdb.IndexReader, ms []*labels.Matcher, maxCardinality int) (selectMatchers, lazyMatchers []*labels.Matcher) {
	if maxCardinality <= 0 || len(ms) < 2 {
		return ms, nil
	}

	hasMetricNameMatcher := false
	for _, m := range ms {
		if m.Name == labels.MetricName && m.Type == labels.MatchEqual {
			hasMetricNameMatcher = true
			break
		}
	}
	if !hasMetricNameMatcher {
		return ms, nil
	}

	// First pass: identify regex matchers that are candidates for deferral and
	// estimate the number of series the selective (equality) matchers would return.
	type regexCandidate struct {
		matcher     *labels.Matcher
		cardinality int
	}

	var candidates []regexCandidate
	selectMatchers = make([]*labels.Matcher, 0, len(ms))
	minSelectPostings := 0

	for _, m := range ms {
		if m.Type == labels.MatchRegexp || m.Type == labels.MatchNotRegexp {
			// Never defer __name__ regex matchers.
			if m.Name == labels.MetricName {
				selectMatchers = append(selectMatchers, m)
				continue
			}

			// Check if the label has high cardinality.
			vals, err := ix.LabelValues(ctx, m.Name, (*storage.LabelHints)(nil))
			if err != nil || len(vals) <= maxCardinality {
				selectMatchers = append(selectMatchers, m)
				continue
			}

			candidates = append(candidates, regexCandidate{matcher: m, cardinality: len(vals)})
			continue
		}

		selectMatchers = append(selectMatchers, m)
		if m.Type == labels.MatchEqual {
			if n := postingsLen(ctx, ix, m.Name, m.Value); n > 0 {
				if minSelectPostings == 0 || n < minSelectPostings {
					minSelectPostings = n
				}
			}
		}
	}

	// Only defer if the selective matchers produce fewer series than the regex
	// label's cardinality (i.e., lazy iteration is cheaper than full scan).
	if len(candidates) == 0 || minSelectPostings == 0 {
		return ms, nil
	}

	for _, c := range candidates {
		if c.cardinality > minSelectPostings {
			lazyMatchers = append(lazyMatchers, c.matcher)
		} else {
			selectMatchers = append(selectMatchers, c.matcher)
		}
	}

	if len(lazyMatchers) == 0 {
		return ms, nil
	}

	return selectMatchers, lazyMatchers
}

// postingsLen returns the number of series matching a single label pair.
// For the head block, Postings() for a single value returns a *ListPostings
// directly, so Len() is O(1) — just a slice length read.
func postingsLen(ctx context.Context, ix prom_tsdb.IndexReader, name, value string) int {
	p, err := ix.Postings(ctx, name, value)
	if err != nil {
		return 0
	}
	if lp, ok := p.(*index.ListPostings); ok {
		return lp.Len()
	}
	return 0
}
