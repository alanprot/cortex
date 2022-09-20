package tripperware

import (
	"context"

	util_log "github.com/cortexproject/cortex/pkg/util/log"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/thanos-io/thanos/pkg/querysharding"
	"github.com/thanos-io/thanos/pkg/store/storepb"

	cquerysharding "github.com/cortexproject/cortex/pkg/querysharding"
)

func ShardByMiddleware(logger log.Logger, limits Limits, merger Merger, numShards int) Middleware {
	return MiddlewareFunc(func(next Handler) Handler {
		return shardBy{
			next:      next,
			limits:    limits,
			merger:    merger,
			logger:    logger,
			numShards: numShards,
		}
	})
}

type shardBy struct {
	next          Handler
	limits        Limits
	logger        log.Logger
	merger        Merger
	queryAnalyzer *querysharding.QueryAnalyzer
	numShards     int
}

func (s shardBy) Do(ctx context.Context, r Request) (Response, error) {
	logger := util_log.WithContext(ctx, s.logger)
	analysis, err := s.queryAnalyzer.Analyze(r.GetQuery())
	if err != nil {
		level.Warn(logger).Log("msg", "error sharding query", "q", r.GetQuery(), "err", err)
	}

	if err != nil || !analysis.IsShardable() {
		return s.next.Do(ctx, r)
	}

	reqs := s.shardQuery(logger, r, analysis)

	reqResps, err := DoRequests(ctx, s.next, reqs, s.limits)
	if err != nil {
		return nil, err
	}

	resps := make([]Response, 0, len(reqResps))
	for _, reqResp := range reqResps {
		resps = append(resps, reqResp.Response)
	}

	response, err := s.merger.MergeResponse(resps...)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (s shardBy) shardQuery(l log.Logger, r Request, analysis querysharding.QueryAnalysis) []Request {
	reqs := make([]Request, s.numShards)
	for i := 0; i < s.numShards; i++ {
		q, err := cquerysharding.InjectShardingInfo(r.GetQuery(), &storepb.ShardInfo{
			TotalShards: int64(s.numShards),
			ShardIndex:  int64(i),
			By:          analysis.ShardBy(),
			Labels:      analysis.ShardingLabels(),
		})
		reqs[i] = r.WithQuery(q)

		if err != nil {
			level.Warn(l).Log("msg", "error sharding query", "q", r.GetQuery(), "err", err)
			return []Request{r}
		}
	}

	return reqs
}
