package tsdb

import (
	"context"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/index"
)

// NewCachedPostingsForMatchersFunc returns a PostingsForMatchers function
// that caches regex evaluation results on label values.
func NewCachedPostingsForMatchersFunc() func(ctx context.Context, ix tsdb.IndexReader, ms ...*labels.Matcher) (index.Postings, error) {
	cache := newRegexMatchCache()
	return newCachedPostingsForMatchersFunc(cache)
}
