package queryrange

import (
	"context"
	"testing"

	cortexpb "github.com/cortexproject/cortex/pkg/cortexpb"
	"github.com/stretchr/testify/require"
)

type mHandler struct {
	Handler
	resps map[string]Response
}

func (h *mHandler) Do(_ context.Context, r Request) (Response, error) {
	return h.resps[r.GetQuery()], nil
}

func Test_wip(t *testing.T) {
	a := splitQueryByOperator{next: &mHandler{resps: map[string]Response{
		"sum(cortex_prometheus_last_evaluation_samples{cluster=\"cell-1\"})": makeResponse("vector", []SampleStream{
			{
				Labels: []cortexpb.LabelAdapter{
					{
						Name:  "__name__",
						Value: "a",
					},
				},
				Samples: []cortexpb.Sample{
					{
						Value:       1,
						TimestampMs: 1,
					},
					{
						Value:       2,
						TimestampMs: 2,
					},
					{
						Value:       3,
						TimestampMs: 3,
					},
				},
			},
		}),
		"avg(cortex_prometheus_last_evaluation_samples{cluster=\"cell-1\"})": makeResponse("vector", []SampleStream{
			{
				Labels: []cortexpb.LabelAdapter{
					{
						Name:  "__name__",
						Value: "b",
					},
				},
				Samples: []cortexpb.Sample{
					{
						Value:       4,
						TimestampMs: 1,
					},
					{
						Value:       5,
						TimestampMs: 2,
					},
					{
						Value:       6,
						TimestampMs: 3,
					},
				},
			},
		}),
		"10": makeResponse("scalar", []SampleStream{
			{
				Labels: []cortexpb.LabelAdapter{},
				Samples: []cortexpb.Sample{
					{
						Value:       10,
						TimestampMs: 1661225820,
					},
				},
			},
		}),
		"30": makeResponse("scalar", []SampleStream{
			{
				Labels: []cortexpb.LabelAdapter{},
				Samples: []cortexpb.Sample{
					{
						Value:       30,
						TimestampMs: 1661225820,
					},
				},
			},
		}),
	}}}
	req := PrometheusRequest{
		Query: "10 + sum(cortex_prometheus_last_evaluation_samples{cluster=\"cell-1\"}) / avg(cortex_prometheus_last_evaluation_samples{cluster=\"cell-1\"}) + 30",
	}
	r, err := a.Do(context.Background(), &req)

	require.NoError(t, err)
	expected := []SampleStream{
		{
			Labels: []cortexpb.LabelAdapter{
				{
					Name:  "__name__",
					Value: "a",
				},
			},
			Samples: []cortexpb.Sample{
				{
					// 10 + 1 / 4 + 30
					Value:       40.25,
					TimestampMs: 1,
				},
				{
					Value:       40.4,
					TimestampMs: 2,
				},
				{
					Value:       40.5,
					TimestampMs: 3,
				},
			},
		},
	}
	require.Equal(t, r.(*PrometheusResponse).Data.Result, expected)
}

func makeResponse(rType string, s []SampleStream) *PrometheusResponse {
	return &PrometheusResponse{
		Status: "success",
		Data: PrometheusData{
			ResultType: rType,
			Result:     s,
		},
	}
}
