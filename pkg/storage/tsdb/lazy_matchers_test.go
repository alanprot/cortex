package tsdb

import (
	"context"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	prom_tsdb "github.com/prometheus/prometheus/tsdb"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/tsdb/index"
	"github.com/stretchr/testify/assert"
)

func TestSplitMatchersForHead(t *testing.T) {
	ctx := context.Background()

	ir := &mockIndexReader{
		labelValues: map[string][]string{
			"__name__":  {"cpu", "memory", "disk"},
			"pod":       generateValues("pod-", 50000),
			"namespace": {"prod", "staging", "dev"},
			"service":   {"api", "worker", "gateway", "frontend", "backend"},
			"job":       {"api", "worker", "gateway"},
		},
		postingsCounts: map[string]int{
			"__name__\xffcpu":      1000,
			"__name__\xffmemory":   800,
			"service\xffapi":       200,
			"service\xffworker":    300,
			"namespace\xffprod":    500,
			"namespace\xffstaging": 300,
			"namespace\xffdev":     200,
		},
	}

	tests := []struct {
		name           string
		matchers       []*labels.Matcher
		maxCardinality int
		wantSelect     int
		wantLazy       int
		wantLazyLabels []string
	}{
		{
			name: "regex on high-cardinality label with selective equality matcher - deferred",
			matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
				labels.MustNewMatcher(labels.MatchEqual, "service", "api"),
				labels.MustNewMatcher(labels.MatchRegexp, "pod", ".*alan.*"),
			},
			maxCardinality: 10000,
			wantSelect:     2, // __name__ + service
			wantLazy:       1,
			wantLazyLabels: []string{"pod"},
		},
		{
			name: "regex on high-cardinality label with only __name__ equality - deferred (name is selective)",
			matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
				labels.MustNewMatcher(labels.MatchRegexp, "pod", ".*alan.*"),
			},
			maxCardinality: 10000,
			wantSelect:     1,
			wantLazy:       1,
			wantLazyLabels: []string{"pod"},
		},
		{
			name: "regex on low-cardinality label - NOT deferred regardless",
			matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
				labels.MustNewMatcher(labels.MatchEqual, "service", "api"),
				labels.MustNewMatcher(labels.MatchRegexp, "namespace", "prod|staging"),
			},
			maxCardinality: 10000,
			wantSelect:     3, // namespace only has 3 values, below threshold
			wantLazy:       0,
		},
		{
			name: "no __name__ matcher - nothing deferred",
			matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchRegexp, "pod", ".*alan.*"),
				labels.MustNewMatcher(labels.MatchEqual, "namespace", "prod"),
			},
			maxCardinality: 10000,
			wantSelect:     2,
			wantLazy:       0,
		},
		{
			name: "disabled when maxCardinality is 0",
			matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
				labels.MustNewMatcher(labels.MatchEqual, "service", "api"),
				labels.MustNewMatcher(labels.MatchRegexp, "pod", ".*alan.*"),
			},
			maxCardinality: 0,
			wantSelect:     3,
			wantLazy:       0,
		},
		{
			name: "negative regex on high-cardinality with selective matcher - deferred",
			matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
				labels.MustNewMatcher(labels.MatchEqual, "namespace", "prod"),
				labels.MustNewMatcher(labels.MatchNotRegexp, "pod", ".*test.*"),
			},
			maxCardinality: 10000,
			wantSelect:     2,
			wantLazy:       1,
			wantLazyLabels: []string{"pod"},
		},
		{
			name: "__name__ regex is never deferred",
			matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
				labels.MustNewMatcher(labels.MatchEqual, "service", "api"),
				labels.MustNewMatcher(labels.MatchRegexp, "__name__", "cpu|memory"),
			},
			maxCardinality: 1,
			wantSelect:     3,
			wantLazy:       0,
		},
		{
			name: "single matcher - nothing deferred",
			matchers: []*labels.Matcher{
				labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
			},
			maxCardinality: 1,
			wantSelect:     1,
			wantLazy:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selectMs, lazyMs := splitMatchersForHead(ctx, ir, tt.matchers, tt.maxCardinality)
			assert.Len(t, selectMs, tt.wantSelect, "select matchers count")
			assert.Len(t, lazyMs, tt.wantLazy, "lazy matchers count")

			for i, name := range tt.wantLazyLabels {
				assert.Equal(t, name, lazyMs[i].Name)
			}
		})
	}
}

func TestFetchWithLazyMatchers(t *testing.T) {
	ctx := context.Background()

	// Build an in-memory head with known series
	ir := &mockIndexReaderWithSeries{
		mockIndexReader: mockIndexReader{
			labelValues: map[string][]string{
				"__name__": {"cpu"},
				"pod":      {"web-1", "web-2", "worker-1", "worker-2", "api-1"},
				"service":  {"frontend", "backend"},
			},
		},
		series: map[storage.SeriesRef]labels.Labels{
			1: labels.FromStrings("__name__", "cpu", "pod", "web-1", "service", "frontend"),
			2: labels.FromStrings("__name__", "cpu", "pod", "web-2", "service", "frontend"),
			3: labels.FromStrings("__name__", "cpu", "pod", "worker-1", "service", "backend"),
			4: labels.FromStrings("__name__", "cpu", "pod", "worker-2", "service", "backend"),
			5: labels.FromStrings("__name__", "cpu", "pod", "api-1", "service", "backend"),
		},
	}

	cache := &blocksPostingsForMatchersCache{
		postingsForMatchersFunc: func(_ context.Context, ix prom_tsdb.IndexReader, ms ...*labels.Matcher) (index.Postings, error) {
			// Simulate: selectMs = [__name__="cpu", service="frontend"] -> returns refs 1, 2
			return index.NewListPostings([]storage.SeriesRef{1, 2, 3, 4, 5}[:2]), nil
		},
	}

	selectMs := []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
		labels.MustNewMatcher(labels.MatchEqual, "service", "frontend"),
	}
	lazyMs := []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchRegexp, "pod", "web.*"),
	}

	refs, size, err := cache.fetchWithLazyMatchers(ctx, ir, selectMs, lazyMs)
	assert.NoError(t, err)
	assert.Equal(t, int64(len(refs)*8), size)
	// Both series 1 and 2 have pod=web-*, so both should match
	assert.Equal(t, []storage.SeriesRef{1, 2}, refs)
}

func TestFetchWithLazyMatchers_FiltersCorrectly(t *testing.T) {
	ctx := context.Background()

	ir := &mockIndexReaderWithSeries{
		mockIndexReader: mockIndexReader{
			labelValues: map[string][]string{
				"pod": {"web-1", "worker-1", "web-2"},
			},
		},
		series: map[storage.SeriesRef]labels.Labels{
			1: labels.FromStrings("pod", "web-1"),
			2: labels.FromStrings("pod", "worker-1"),
			3: labels.FromStrings("pod", "web-2"),
		},
	}

	cache := &blocksPostingsForMatchersCache{
		postingsForMatchersFunc: func(_ context.Context, _ prom_tsdb.IndexReader, _ ...*labels.Matcher) (index.Postings, error) {
			return index.NewListPostings([]storage.SeriesRef{1, 2, 3}), nil
		},
	}

	selectMs := []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchEqual, "__name__", "cpu"),
	}
	lazyMs := []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchRegexp, "pod", "web.*"),
	}

	refs, _, err := cache.fetchWithLazyMatchers(ctx, ir, selectMs, lazyMs)
	assert.NoError(t, err)
	assert.Equal(t, []storage.SeriesRef{1, 3}, refs)
}

// --- Mocks ---

type mockIndexReader struct {
	prom_tsdb.IndexReader
	labelValues    map[string][]string
	postingsCounts map[string]int // "name\xffvalue" -> count
}

func (m *mockIndexReader) LabelValues(_ context.Context, name string, _ *storage.LabelHints, _ ...*labels.Matcher) ([]string, error) {
	return m.labelValues[name], nil
}

func (m *mockIndexReader) Close() error              { return nil }
func (m *mockIndexReader) Symbols() index.StringIter { return nil }
func (m *mockIndexReader) LabelNames(_ context.Context, _ ...*labels.Matcher) ([]string, error) {
	return nil, nil
}
func (m *mockIndexReader) SortedLabelValues(_ context.Context, _ string, _ *storage.LabelHints, _ ...*labels.Matcher) ([]string, error) {
	return nil, nil
}
func (m *mockIndexReader) Postings(_ context.Context, name string, values ...string) (index.Postings, error) {
	if m.postingsCounts != nil && len(values) == 1 {
		key := name + "\xff" + values[0]
		if n, ok := m.postingsCounts[key]; ok {
			refs := make([]storage.SeriesRef, n)
			for i := range refs {
				refs[i] = storage.SeriesRef(i + 1)
			}
			return index.NewListPostings(refs), nil
		}
	}
	return index.EmptyPostings(), nil
}
func (m *mockIndexReader) PostingsForLabelMatching(_ context.Context, _ string, _ func(string) bool) index.Postings {
	return index.EmptyPostings()
}
func (m *mockIndexReader) PostingsForAllLabelValues(_ context.Context, _ string) index.Postings {
	return index.EmptyPostings()
}
func (m *mockIndexReader) SortedPostings(p index.Postings) index.Postings               { return p }
func (m *mockIndexReader) ShardedPostings(p index.Postings, _, _ uint64) index.Postings { return p }
func (m *mockIndexReader) Series(_ storage.SeriesRef, _ *labels.ScratchBuilder, _ *[]chunks.Meta) error {
	return nil
}
func (m *mockIndexReader) LabelValueFor(_ context.Context, _ storage.SeriesRef, _ string) (string, error) {
	return "", storage.ErrNotFound
}
func (m *mockIndexReader) LabelNamesFor(_ context.Context, _ index.Postings) ([]string, error) {
	return nil, nil
}

// mockIndexReaderWithSeries extends mockIndexReader with series label data
type mockIndexReaderWithSeries struct {
	mockIndexReader
	series map[storage.SeriesRef]labels.Labels
}

func (m *mockIndexReaderWithSeries) LabelValueFor(_ context.Context, id storage.SeriesRef, label string) (string, error) {
	lbls, ok := m.series[id]
	if !ok {
		return "", storage.ErrNotFound
	}
	v := lbls.Get(label)
	if v == "" {
		return "", storage.ErrNotFound
	}
	return v, nil
}

func generateValues(prefix string, count int) []string {
	vals := make([]string, count)
	for i := range vals {
		vals[i] = prefix + string(rune('0'+i%10)) + string(rune('0'+i/10%10))
	}
	return vals
}
