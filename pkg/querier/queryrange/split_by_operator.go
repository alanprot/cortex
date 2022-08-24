package queryrange

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"

	"github.com/prometheus/prometheus/promql/parser"
	"github.com/weaveworks/common/httpgrpc"
	"golang.org/x/sync/errgroup"
)

type splitQueryByOperator struct {
	next Handler
}

func (s *splitQueryByOperator) Do(ctx context.Context, r Request) (Response, error) {
	expr, err := parser.ParseExpr(r.GetQuery())
	g, ctx := errgroup.WithContext(ctx)
	if err != nil {
		return nil, httpgrpc.Errorf(http.StatusBadRequest, "%s", err)
	}

	queries := s.splitQuery(expr)
	m := sync.Mutex{}
	rMap := map[string]Response{}

	for _, q := range queries {
		func(query string) {
			g.Go(func() error {
				r, err := s.next.Do(ctx, r.WithQuery(query))
				m.Lock()
				rMap[query] = r
				m.Unlock()
				return err
			})
		}(q.String())
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return s.evaluate(expr, rMap), nil
}

func (s *splitQueryByOperator) evaluate(expr parser.Expr, rMap map[string]Response) Response {
	switch e := expr.(type) {
	case *parser.BinaryExpr:
		rr := s.evaluate(e.RHS, rMap)
		rl := s.evaluate(e.LHS, rMap)
		rMap[e.RHS.String()] = rr
		rMap[e.LHS.String()] = rl
		return s.merge(e.Op, rl.(*PrometheusResponse), rr.(*PrometheusResponse))
	case *parser.ParenExpr:
		return s.evaluate(e.Expr, rMap)
	default:
		return rMap[e.String()]
	}
}

func (s *splitQueryByOperator) merge(op parser.ItemType, ltResp, rtResp *PrometheusResponse) Response {
	switch lt, rt := parser.ValueType(ltResp.Data.ResultType), parser.ValueType(rtResp.Data.ResultType); {
	case lt == parser.ValueTypeVector && rt == parser.ValueTypeVector:
		for i, stream := range ltResp.Data.Result {
			for j, sample := range stream.Samples {
				ltResp.Data.Result[i].Samples[j].Value = s.scalarBinop(op, sample.Value, rtResp.Data.Result[i].Samples[j].Value)
			}
		}
		return ltResp
	case lt == parser.ValueTypeVector && rt == parser.ValueTypeScalar:
		for i, stream := range ltResp.Data.Result {
			for j, sample := range stream.Samples {
				ltResp.Data.Result[i].Samples[j].Value = s.scalarBinop(op, sample.Value, rtResp.Data.Result[0].Samples[0].Value)
			}
		}
		return ltResp
	case lt == parser.ValueTypeScalar && rt == parser.ValueTypeScalar:
		for i, stream := range ltResp.Data.Result {
			for j, sample := range stream.Samples {
				ltResp.Data.Result[i].Samples[j].Value = s.scalarBinop(op, sample.Value, rtResp.Data.Result[0].Samples[0].Value)
			}
		}
		return ltResp
	case lt == parser.ValueTypeScalar && rt == parser.ValueTypeVector:
		for i, stream := range rtResp.Data.Result {
			for j, sample := range stream.Samples {
				rtResp.Data.Result[i].Samples[j].Value = s.scalarBinop(op, ltResp.Data.Result[0].Samples[0].Value, sample.Value)
			}
		}
		return rtResp
	default:
		return nil
	}
	return nil
}

func (s *splitQueryByOperator) scalarBinop(op parser.ItemType, lhs, rhs float64) float64 {
	switch op {
	case parser.ADD:
		return lhs + rhs
	case parser.SUB:
		return lhs - rhs
	case parser.MUL:
		return lhs * rhs
	case parser.DIV:
		return lhs / rhs
	case parser.POW:
		return math.Pow(lhs, rhs)
	case parser.MOD:
		return math.Mod(lhs, rhs)
	case parser.EQLC:
		return btos(lhs == rhs)
	case parser.NEQ:
		return btos(lhs != rhs)
	case parser.GTR:
		return btos(lhs > rhs)
	case parser.LSS:
		return btos(lhs < rhs)
	case parser.GTE:
		return btos(lhs >= rhs)
	case parser.LTE:
		return btos(lhs <= rhs)
	case parser.ATAN2:
		return math.Atan2(lhs, rhs)
	}
	panic(fmt.Errorf("operator %q not allowed for Scalar operations", op))
}

func btos(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (s *splitQueryByOperator) splitQuery(expr parser.Expr) []parser.Expr {
	switch e := expr.(type) {
	case *parser.BinaryExpr:
		return append(s.splitQuery(e.LHS), s.splitQuery(e.RHS)...)
	case *parser.ParenExpr:
		return s.splitQuery(e.Expr)
	case *parser.AggregateExpr:
		return []parser.Expr{expr}
	default:
		return []parser.Expr{expr}
	}
}
