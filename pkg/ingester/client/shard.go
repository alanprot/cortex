package client

import (
	"github.com/prometheus/prometheus/model/labels"

	"github.com/cortexproject/cortex/pkg/cortexpb"
)

// ShardByUser returns the token of a tenant.
func ShardByUser(userID string) uint32 {
	return HashAdd32(HashNew32(), userID)
}

// ShardByMetricName returns the token of a metric. metricName is not retained.
func ShardByMetricName(userID string, metricName string) uint32 {
	return HashAdd32(ShardByUser(userID), metricName)
}

// ShardByAllLabelAdapters returns the token the distributor routes a series
// with when sharding by all labels. It depends on label order and skips empty values.
func ShardByAllLabelAdapters(userID string, lbls []cortexpb.LabelAdapter) uint32 {
	h := ShardByUser(userID)
	for _, l := range lbls {
		if len(l.Value) > 0 {
			h = HashAdd32(h, l.Name)
			h = HashAdd32(h, l.Value)
		}
	}
	return h
}

// ShardByAllLabels is ShardByAllLabelAdapters for labels.Labels, so the
// ingester computes the same token for the series it stores.
func ShardByAllLabels(userID string, lbls labels.Labels) uint32 {
	h := ShardByUser(userID)
	lbls.Range(func(l labels.Label) {
		if len(l.Value) > 0 {
			h = HashAdd32(h, l.Name)
			h = HashAdd32(h, l.Value)
		}
	})
	return h
}
