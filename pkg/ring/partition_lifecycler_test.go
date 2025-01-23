package ring

import (
	"context"
	"github.com/cortexproject/cortex/pkg/ring/kv/consul"
	"github.com/cortexproject/cortex/pkg/util/flagext"
	"github.com/go-kit/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestIngesterPartitionStart(t *testing.T) {
	ringStore, closer := consul.NewInMemoryClient(GetPartitionCodec(), log.NewNopLogger(), nil)
	t.Cleanup(func() { assert.NoError(t, closer.Close()) })
	var ringConfig Config
	flagext.DefaultValues(&ringConfig)
	ringConfig.KVStore.Mock = ringStore
	lifecyclerConfig1 := testLifecyclerConfig(ringConfig, "ing1")
	lifecyclerConfig2 := testLifecyclerConfig(ringConfig, "ing2")

	lf, err := NewPartitionLifecycler(lifecyclerConfig1, log.NewNopLogger(), "partition", "partition", nil)
	require.NoError(t, err)
	lf2, err := NewPartitionLifecycler(lifecyclerConfig2, log.NewNopLogger(), "partition", "partition", nil)
	require.NoError(t, err)
	require.NoError(t, lf.Join(context.Background()))
	require.NoError(t, lf2.Join(context.Background()))
	time.Sleep(100 * time.Second)
}
