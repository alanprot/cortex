package ring

import (
	"fmt"
	"github.com/gogo/protobuf/proto"
	"sort"
	"time"

	"github.com/cortexproject/cortex/pkg/ring/kv/codec"
	"github.com/cortexproject/cortex/pkg/ring/kv/memberlist"
)

// GetPartitionCodec returns the codec used to encode and decode data being put by ring.
func GetPartitionCodec() codec.Codec {
	return codec.NewProtoCodec("partitionRingDesc", func() proto.Message {
		return NewPartitionRingDesc()
	})
}

var (
	_ codec.MultiKey       = &PartitionRingDesc{}
	_ memberlist.Mergeable = &PartitionRingDesc{}
)

func NewPartitionRingDesc() *PartitionRingDesc {
	return &PartitionRingDesc{
		Partitions: map[string]*PartitionDesc{},
	}
}

func (d *PartitionRingDesc) EnsurePartitionCount(c int) {
	if d.Partitions == nil {
		d.Partitions = map[string]*PartitionDesc{}
	}

	for i := len(d.Partitions); i < c; i++ {
		d.Partitions[d.GetIdByIndex(i)] = &PartitionDesc{
			State: P_PENDING,
		}
	}
}

func (d *PartitionRingDesc) Merge(other memberlist.Mergeable, localCAS bool) (change memberlist.Mergeable, error error) {
	//TODO implement me
	panic("implement me")
}

func (d *PartitionRingDesc) GetTokens() []uint32 {
	partitions := make([][]uint32, 0, len(d.Partitions))
	for _, instance := range d.Partitions {
		// Tokens may not be sorted for an older version which, so we enforce sorting here.
		tokens := instance.Tokens
		if !sort.IsSorted(Tokens(tokens)) {
			sort.Sort(Tokens(tokens))
		}

		partitions = append(partitions, tokens)
	}

	return MergeTokens(partitions)
}

func (d *PartitionRingDesc) getTokensInfo() map[uint32]partitionInfo {
	out := map[uint32]partitionInfo{}

	for id, p := range d.Partitions {
		info := partitionInfo{
			partitionID:   id,
			instancesDesc: p.Instances,
		}

		for _, token := range p.Tokens {
			out[token] = info
		}
	}

	return out
}

func (d *PartitionRingDesc) GetInstances() map[string]genericRingInstance {
	r := make(map[string]genericRingInstance, len(d.Partitions))
	for k, v := range d.Partitions {
		r[k] = v
	}
	return r
}

func (d *PartitionRingDesc) MergeContent() []string {
	//TODO implement me
	panic("implement me")
}

func (d *PartitionRingDesc) RemoveTombstones(limit time.Time) (total, removed int) {
	//TODO implement me
	panic("implement me")
}

func (d *PartitionRingDesc) Clone() interface{} {
	r := &PartitionRingDesc{}
	b, _ := d.Marshal()
	_ = proto.Unmarshal(b, r)
	return r
}

func (d *PartitionRingDesc) SplitByID() map[string]interface{} {
	out := make(map[string]interface{}, len(d.Partitions))
	for key := range d.Partitions {
		out[key] = d.Partitions[key]
	}
	return out
}

func (d *PartitionRingDesc) GetIdByIndex(index int) string {
	return fmt.Sprintf("p-%d", index)
}

func (d *PartitionRingDesc) HasInstance(id string) bool {
	for _, p := range d.Partitions {
		if p.Instances != nil {
			_, ok := p.Instances[id]
			return ok
		}
	}

	return false
}

func (d *PartitionRingDesc) JoinIds(in map[string]interface{}) {
	for key, value := range in {
		d.Partitions[key] = value.(*PartitionDesc)
	}
}

func (d *PartitionRingDesc) GetItemFactory() proto.Message {
	return &PartitionDesc{}
}

func (d *PartitionRingDesc) FindDifference(o codec.MultiKey) (interface{}, []string, error) {
	out, ok := o.(*PartitionRingDesc)
	fmt.Printf("partition ring desc (o): %v\n", o)
	fmt.Printf("partition ring desc (d): %v\n", d)
	if !ok {
		// This method only deals with non-nil rings.
		return nil, nil, fmt.Errorf("expected *ring.Desc, got %T", out)
	}

	toUpdated := NewPartitionRingDesc()
	toDelete := make([]string, 0)
	// If both are null
	if d == nil && out == nil {
		return toUpdated, toDelete, nil
	}

	// If new data is empty
	if out == nil {
		for k := range d.Partitions {
			toDelete = append(toDelete, k)
		}
		return toUpdated, toDelete, nil
	}

	//If existent data is empty
	if d == nil {
		for key, value := range out.Partitions {
			toUpdated.Partitions[key] = value
		}
		return toUpdated, toDelete, nil
	}

	//If new added
	for name, oing := range out.Partitions {
		if _, ok := d.Partitions[name]; !ok {
			toUpdated.Partitions[name] = oing
		}
	}

	// If removed or updated
	for name, ing := range d.Partitions {
		oing, ok := out.Partitions[name]
		if !ok {
			toDelete = append(toDelete, name)
		} else if !ing.Equal(oing) {
			if _, ok := toUpdated.Partitions[name]; !ok {
				toUpdated.Partitions[name] = &PartitionDesc{
					State:     oing.State,
					Timestamp: oing.Timestamp,
					Tokens:    oing.Tokens,
					Instances: map[string]InstanceDesc{},
				}
			}
			for s, desc := range oing.Instances {
				toUpdated.Partitions[name].Instances[s] = desc
			}
		}
	}

	fmt.Printf("partition ring desc: %v\n", toUpdated)
	return toUpdated, toDelete, nil
}

func (d *PartitionRingDesc) AddPartition(id string, s PartitionState, tokens []uint32, registeredAt time.Time) {
	if d.Partitions == nil {
		d.Partitions = map[string]*PartitionDesc{}
	}

	registeredTimestamp := int64(0)
	if !registeredAt.IsZero() {
		registeredTimestamp = registeredAt.Unix()
	}

	d.Partitions[id] = &PartitionDesc{
		Tokens:              tokens,
		RegisteredTimestamp: registeredTimestamp,
		State:               s,
	}
}

// AddIngester adds the given ingester to the ring. Ingester will only use supplied tokens,
// any other tokens are removed.
func (m *PartitionDesc) AddIngester(id, addr, zone string) InstanceDesc {
	if m.Instances == nil {
		m.Instances = map[string]InstanceDesc{}
	}

	ingester := InstanceDesc{
		Addr:      addr,
		State:     ACTIVE,
		Timestamp: time.Now().Unix(),
		Zone:      zone,
	}

	m.Instances[id] = ingester
	return ingester
}

func (m *PartitionDesc) GetZone() string {
	return ""
}
