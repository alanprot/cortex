package tsdb

import (
	"context"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	prom_tsdb "github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/index"
)

// NewIncrementalHeadPostingsCacheForTest creates an incremental cache for benchmarking.
func NewIncrementalHeadPostingsCacheForTest() *IncrementalHeadPostingsCache {
	return &IncrementalHeadPostingsCache{
		c: newIncrementalHeadPostingsCache(prom_tsdb.PostingsForMatchers),
	}
}

// IncrementalHeadPostingsCache is the exported wrapper for testing.
type IncrementalHeadPostingsCache struct {
	c *incrementalHeadPostingsCache
}

func (w *IncrementalHeadPostingsCache) PostingsForMatchers(ctx context.Context, ix prom_tsdb.IndexReader, ms ...*labels.Matcher) ([]storage.SeriesRef, error) {
	return w.c.PostingsForMatchers(ctx, ix, ms...)
}

func (w *IncrementalHeadPostingsCache) Clear() {
	w.c.Clear()
}

// Ensure index import is used
var _ index.Postings
