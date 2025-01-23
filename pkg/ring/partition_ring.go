package ring

import (
	"context"
	"fmt"
	"github.com/cortexproject/cortex/pkg/ring/kv"
	"github.com/cortexproject/cortex/pkg/util"
	"github.com/cortexproject/cortex/pkg/util/services"
	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"sync"
)

var (
	_ ReadRing = &PartitionRing{}
)

type PartitionRing struct {
	ReadRing
	services.Service

	key      string
	strategy ReplicationStrategy
	cfg      Config
	KVClient kv.Client

	mtx                  sync.RWMutex
	desc                 *PartitionRingDesc
	ringTokens           []uint32
	ringPartitionByToken map[uint32]partitionInfo
}

type partitionInfo struct {
	partitionID   string
	instancesDesc map[string]InstanceDesc
}

func (r *PartitionRing) Get(key uint32, op Operation, bufDescs []InstanceDesc, bufHosts []string, bufZones map[string]int) (ReplicationSet, error) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()
	if r.desc == nil || len(r.ringTokens) == 0 {
		return ReplicationSet{}, ErrEmptyRing
	}

	var (
		replicationFactor = r.cfg.ReplicationFactor
		instances         = bufDescs[:0]
		start             = searchToken(r.ringTokens, key)
		iterations        = 0

		// We use a slice instead of a map because it's faster to search within a
		// slice than lookup a map for a very low number of items.
		distinctPartitions = bufHosts[:0]
	)

	for i := start; len(instances) < replicationFactor && iterations < len(r.ringTokens); i++ {
		iterations++
		// Wrap i around in the ring.
		i %= len(r.ringTokens)
		token := r.ringTokens[i]

		info, ok := r.ringPartitionByToken[token]
		if !ok {
			// This should never happen unless a bug in the ring code.
			return ReplicationSet{}, ErrInconsistentTokensInfo
		}

		// We want n *distinct* instances.
		if util.StringsContain(distinctPartitions, info.partitionID) {
			continue
		}

		for _, inst := range info.instancesDesc {
			instances = append(instances, inst)
		}

		distinctPartitions = append(distinctPartitions, info.partitionID)
	}

	healthyInstances, maxFailure, err := r.strategy.Filter(instances, op, r.cfg.ReplicationFactor, r.cfg.HeartbeatTimeout, r.cfg.ZoneAwarenessEnabled, r.KVClient.LastUpdateTime(r.key))
	if err != nil {
		return ReplicationSet{}, err
	}

	return ReplicationSet{
		Instances: healthyInstances,
		MaxErrors: maxFailure,
	}, nil
}

func (r *PartitionRing) ShuffleShard(identifier string, size int) ReadRing {
	return r
}

func (r *PartitionRing) ShuffleShardWithZoneStability(identifier string, size int) ReadRing {
	return r
}

func NewPartitionRing(cfg Config, ring *Ring, name, key string, logger log.Logger, reg prometheus.Registerer) (*PartitionRing, error) {
	// Suffix all client names with "-ring" to denote this kv client is used by the ring
	store, err := kv.NewClient(
		cfg.KVStore,
		GetPartitionCodec(),
		kv.RegistererWithKVName(reg, name+"partition-ring"),
		logger,
	)
	if err != nil {
		return nil, err
	}

	r := &PartitionRing{
		cfg:      cfg,
		key:      key,
		KVClient: store,
		strategy: ring.strategy,
		ReadRing: ring,
	}
	r.Service = services.NewBasicService(r.starting, r.loop, nil).WithName(fmt.Sprintf("%s partition ring client", name))
	return r, nil
}

func (r *PartitionRing) starting(ctx context.Context) error {
	_, err := r.KVClient.Get(ctx, r.key)
	if err != nil {
		return err
	}

	return nil
}

func (r *PartitionRing) loop(ctx context.Context) error {
	var partitionDesc *PartitionRingDesc
	r.KVClient.WatchKey(ctx, r.key, func(in interface{}) bool {
		if in == nil {
			partitionDesc = NewPartitionRingDesc()
		} else {
			partitionDesc = in.(*PartitionRingDesc)
		}

		ringTokens := partitionDesc.GetTokens()
		ringPartitionByToken := partitionDesc.getTokensInfo()
		r.mtx.Lock()
		r.desc = partitionDesc
		r.ringPartitionByToken = ringPartitionByToken
		r.ringTokens = ringTokens
		r.mtx.Unlock()
		return true
	})

	return nil
}
