package ring

import (
	"context"
	"fmt"
	"github.com/cortexproject/cortex/pkg/ring/kv"
	"github.com/cortexproject/cortex/pkg/util/services"
	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"sort"
	"time"
)

type PartitionLifecycler struct {
	*services.BasicService

	KVStore kv.Client
	Addr    string

	ringKey string
	cfg     LifecyclerConfig
	tg      TokenGenerator
}

func NewPartitionLifecycler(
	cfg LifecyclerConfig,
	logger log.Logger,
	ringName, ringKey string,
	reg prometheus.Registerer,
) (*PartitionLifecycler, error) {
	store, err := kv.NewClient(
		cfg.RingConfig.KVStore,
		GetPartitionCodec(),
		kv.RegistererWithKVName(reg, ringName+"-partition-lifecycler"),
		logger,
	)
	if err != nil {
		return nil, err
	}

	addr, err := GetInstanceAddr(cfg.Addr, cfg.InfNames, logger)
	if err != nil {
		return nil, err
	}

	port := GetInstancePort(cfg.Port, cfg.ListenPort)

	lc := &PartitionLifecycler{
		KVStore: store,
		ringKey: ringKey,
		cfg:     cfg,
		Addr:    fmt.Sprintf("%s:%d", addr, port),
		tg:      NewMinimizeSpreadTokenGenerator(),
	}

	lc.BasicService = services.
		NewBasicService(nil, lc.loop, lc.stopping).
		WithName(fmt.Sprintf("%s ring partition lifecycler", ringName))

	return lc, nil
}

func (l *PartitionLifecycler) Join(ctx context.Context) error {
	var partitionDesc *PartitionRingDesc

	err := l.KVStore.CAS(ctx, l.ringKey, func(in interface{}) (out interface{}, retry bool, err error) {
		if in == nil {
			partitionDesc = NewPartitionRingDesc()
		} else {
			partitionDesc = in.(*PartitionRingDesc)
		}

		l.ensureRegistered(partitionDesc)

		return partitionDesc, false, nil
	})

	return err
}

func (l *PartitionLifecycler) ensureRegistered(partitionDesc *PartitionRingDesc) string {
	fmt.Printf("Partition Ring %v\n", partitionDesc)

	if partitionDesc.Partitions == nil {
		partitionDesc.Partitions = map[string]*PartitionDesc{}
	}

	partitionsByAz := map[string]map[string]struct{}{}

	for pId, p := range partitionDesc.Partitions {
		partitionsByAz[pId] = map[string]struct{}{}
		for id, desc := range p.Instances {
			if id == l.cfg.ID {
				desc.Timestamp = time.Now().Unix()
				desc.Addr = l.Addr
				desc.Zone = l.cfg.Zone
				partitionDesc.Partitions[pId].Instances[id] = desc
				return pId
			}
			partitionsByAz[pId][desc.Zone] = struct{}{}
		}
	}

	for pId, _ := range partitionDesc.Partitions {
		if _, ok := partitionsByAz[pId][l.cfg.Zone]; ok {
			continue
		}
		partitionDesc.Partitions[pId].AddIngester(l.cfg.ID, l.Addr, l.cfg.Zone)
		return pId
	}

	nPartitionsId := fmt.Sprintf("p-%v", len(partitionDesc.Partitions))

	var myTokens Tokens
	myTokens = l.tg.GenerateTokens(partitionDesc, nPartitionsId, "", 512, true)
	sort.Sort(myTokens)
	partitionDesc.AddPartition(nPartitionsId, P_PENDING, myTokens, time.Now())
	return nPartitionsId
}

func (l *PartitionLifecycler) heartBeat(ctx context.Context) error {
	var partitionDesc *PartitionRingDesc
	fmt.Printf("Partition Ring hearbeat %v\n", l.ringKey)

	err := l.KVStore.CAS(ctx, l.ringKey, func(in interface{}) (out interface{}, retry bool, err error) {
		if in == nil {
			partitionDesc = NewPartitionRingDesc()
		} else {
			partitionDesc = in.(*PartitionRingDesc)
		}

		pId := l.ensureRegistered(partitionDesc)
		for id, desc := range partitionDesc.Partitions[pId].Instances {
			// Already have another instance in the same AZ
			if id != l.cfg.ID && desc.Zone == l.cfg.Zone {
				if l.cfg.ID < id {
					fmt.Printf("Removing from partition %v ingesterId: %v\n", pId, l.cfg.ID)
					delete(partitionDesc.Partitions[pId].Instances, l.cfg.ID)
					fmt.Printf("Removed from partition %v\n", partitionDesc)
				}
			}
		}

		p := partitionDesc.Partitions[pId]
		p.Timestamp = time.Now().Unix()
		partitionDesc.Partitions[pId] = p

		return partitionDesc, false, nil
	})

	return err
}

func (l *PartitionLifecycler) stopping(err error) error {
	return err
}

func (l *PartitionLifecycler) loop(ctx context.Context) error {
	t := time.NewTicker(1 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			l.heartBeat(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
