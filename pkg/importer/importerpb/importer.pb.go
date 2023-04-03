// Code generated by protoc-gen-gogo. DO NOT EDIT.
// source: importer.proto

package importerpb

import (
	context "context"
	fmt "fmt"
	_ "github.com/cortexproject/cortex/pkg/cortexpb"
	_ "github.com/gogo/protobuf/gogoproto"
	proto "github.com/gogo/protobuf/proto"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	io "io"
	math "math"
	math_bits "math/bits"
	reflect "reflect"
	strings "strings"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.GoGoProtoPackageIsVersion3 // please upgrade the proto package

type SampleRequest struct {
	SampleField string `protobuf:"bytes,1,opt,name=sampleField,proto3" json:"sampleField,omitempty"`
}

func (m *SampleRequest) Reset()      { *m = SampleRequest{} }
func (*SampleRequest) ProtoMessage() {}
func (*SampleRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_394990ff89e0d02b, []int{0}
}
func (m *SampleRequest) XXX_Unmarshal(b []byte) error {
	return m.Unmarshal(b)
}
func (m *SampleRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	if deterministic {
		return xxx_messageInfo_SampleRequest.Marshal(b, m, deterministic)
	} else {
		b = b[:cap(b)]
		n, err := m.MarshalToSizedBuffer(b)
		if err != nil {
			return nil, err
		}
		return b[:n], nil
	}
}
func (m *SampleRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_SampleRequest.Merge(m, src)
}
func (m *SampleRequest) XXX_Size() int {
	return m.Size()
}
func (m *SampleRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_SampleRequest.DiscardUnknown(m)
}

var xxx_messageInfo_SampleRequest proto.InternalMessageInfo

func (m *SampleRequest) GetSampleField() string {
	if m != nil {
		return m.SampleField
	}
	return ""
}

type SampleResponse struct {
	SampleField string `protobuf:"bytes,1,opt,name=sampleField,proto3" json:"sampleField,omitempty"`
}

func (m *SampleResponse) Reset()      { *m = SampleResponse{} }
func (*SampleResponse) ProtoMessage() {}
func (*SampleResponse) Descriptor() ([]byte, []int) {
	return fileDescriptor_394990ff89e0d02b, []int{1}
}
func (m *SampleResponse) XXX_Unmarshal(b []byte) error {
	return m.Unmarshal(b)
}
func (m *SampleResponse) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	if deterministic {
		return xxx_messageInfo_SampleResponse.Marshal(b, m, deterministic)
	} else {
		b = b[:cap(b)]
		n, err := m.MarshalToSizedBuffer(b)
		if err != nil {
			return nil, err
		}
		return b[:n], nil
	}
}
func (m *SampleResponse) XXX_Merge(src proto.Message) {
	xxx_messageInfo_SampleResponse.Merge(m, src)
}
func (m *SampleResponse) XXX_Size() int {
	return m.Size()
}
func (m *SampleResponse) XXX_DiscardUnknown() {
	xxx_messageInfo_SampleResponse.DiscardUnknown(m)
}

var xxx_messageInfo_SampleResponse proto.InternalMessageInfo

func (m *SampleResponse) GetSampleField() string {
	if m != nil {
		return m.SampleField
	}
	return ""
}

func init() {
	proto.RegisterType((*SampleRequest)(nil), "importer.SampleRequest")
	proto.RegisterType((*SampleResponse)(nil), "importer.SampleResponse")
}

func init() { proto.RegisterFile("importer.proto", fileDescriptor_394990ff89e0d02b) }

var fileDescriptor_394990ff89e0d02b = []byte{
	// 244 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xe2, 0xe2, 0xcb, 0xcc, 0x2d, 0xc8,
	0x2f, 0x2a, 0x49, 0x2d, 0xd2, 0x2b, 0x28, 0xca, 0x2f, 0xc9, 0x17, 0xe2, 0x80, 0xf1, 0xa5, 0x44,
	0xd2, 0xf3, 0xd3, 0xf3, 0xc1, 0x82, 0xfa, 0x20, 0x16, 0x44, 0x5e, 0xca, 0x32, 0x3d, 0xb3, 0x24,
	0xa3, 0x34, 0x49, 0x2f, 0x39, 0x3f, 0x57, 0x3f, 0x19, 0xa4, 0xb0, 0xa2, 0xa0, 0x28, 0x3f, 0x2b,
	0x35, 0xb9, 0x04, 0xca, 0xd3, 0x2f, 0xc8, 0x4e, 0x87, 0x49, 0x24, 0x41, 0x19, 0x10, 0xad, 0x4a,
	0x86, 0x5c, 0xbc, 0xc1, 0x89, 0xb9, 0x05, 0x39, 0xa9, 0x41, 0xa9, 0x85, 0xa5, 0xa9, 0xc5, 0x25,
	0x42, 0x0a, 0x5c, 0xdc, 0xc5, 0x60, 0x01, 0xb7, 0xcc, 0xd4, 0x9c, 0x14, 0x09, 0x46, 0x05, 0x46,
	0x0d, 0xce, 0x20, 0x64, 0x21, 0x25, 0x23, 0x2e, 0x3e, 0x98, 0x96, 0xe2, 0x82, 0xfc, 0xbc, 0xe2,
	0x54, 0xc2, 0x7a, 0x8c, 0x7c, 0xb8, 0x38, 0x3c, 0xa1, 0x7e, 0x10, 0x72, 0xe0, 0xe2, 0x84, 0xea,
	0x0f, 0x70, 0x16, 0x12, 0xd7, 0x83, 0xfb, 0x15, 0xc5, 0x1d, 0x52, 0x12, 0x98, 0x12, 0x10, 0xdb,
	0x94, 0x18, 0x9c, 0x1c, 0x2e, 0x3c, 0x94, 0x63, 0xb8, 0xf1, 0x50, 0x8e, 0xe1, 0xc3, 0x43, 0x39,
	0xc6, 0x86, 0x47, 0x72, 0x8c, 0x2b, 0x1e, 0xc9, 0x31, 0x9e, 0x78, 0x24, 0xc7, 0x78, 0xe1, 0x91,
	0x1c, 0xe3, 0x83, 0x47, 0x72, 0x8c, 0x2f, 0x1e, 0xc9, 0x31, 0x7c, 0x78, 0x24, 0xc7, 0x38, 0xe1,
	0xb1, 0x1c, 0xc3, 0x85, 0xc7, 0x72, 0x0c, 0x37, 0x1e, 0xcb, 0x31, 0x44, 0x71, 0xc1, 0x0c, 0x2c,
	0x48, 0x4a, 0x62, 0x03, 0xfb, 0xde, 0x18, 0x10, 0x00, 0x00, 0xff, 0xff, 0xe8, 0xf1, 0x44, 0x73,
	0x6a, 0x01, 0x00, 0x00,
}

func (this *SampleRequest) Equal(that interface{}) bool {
	if that == nil {
		return this == nil
	}

	that1, ok := that.(*SampleRequest)
	if !ok {
		that2, ok := that.(SampleRequest)
		if ok {
			that1 = &that2
		} else {
			return false
		}
	}
	if that1 == nil {
		return this == nil
	} else if this == nil {
		return false
	}
	if this.SampleField != that1.SampleField {
		return false
	}
	return true
}
func (this *SampleResponse) Equal(that interface{}) bool {
	if that == nil {
		return this == nil
	}

	that1, ok := that.(*SampleResponse)
	if !ok {
		that2, ok := that.(SampleResponse)
		if ok {
			that1 = &that2
		} else {
			return false
		}
	}
	if that1 == nil {
		return this == nil
	} else if this == nil {
		return false
	}
	if this.SampleField != that1.SampleField {
		return false
	}
	return true
}
func (this *SampleRequest) GoString() string {
	if this == nil {
		return "nil"
	}
	s := make([]string, 0, 5)
	s = append(s, "&importerpb.SampleRequest{")
	s = append(s, "SampleField: "+fmt.Sprintf("%#v", this.SampleField)+",\n")
	s = append(s, "}")
	return strings.Join(s, "")
}
func (this *SampleResponse) GoString() string {
	if this == nil {
		return "nil"
	}
	s := make([]string, 0, 5)
	s = append(s, "&importerpb.SampleResponse{")
	s = append(s, "SampleField: "+fmt.Sprintf("%#v", this.SampleField)+",\n")
	s = append(s, "}")
	return strings.Join(s, "")
}
func valueToGoStringImporter(v interface{}, typ string) string {
	rv := reflect.ValueOf(v)
	if rv.IsNil() {
		return "nil"
	}
	pv := reflect.Indirect(rv).Interface()
	return fmt.Sprintf("func(v %v) *%v { return &v } ( %#v )", typ, typ, pv)
}

// Reference imports to suppress errors if they are not otherwise used.
var _ context.Context
var _ grpc.ClientConn

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
const _ = grpc.SupportPackageIsVersion4

// ImporterClient is the client API for Importer service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://godoc.org/google.golang.org/grpc#ClientConn.NewStream.
type ImporterClient interface {
	SampleRPC(ctx context.Context, in *SampleRequest, opts ...grpc.CallOption) (*SampleResponse, error)
}

type importerClient struct {
	cc *grpc.ClientConn
}

func NewImporterClient(cc *grpc.ClientConn) ImporterClient {
	return &importerClient{cc}
}

func (c *importerClient) SampleRPC(ctx context.Context, in *SampleRequest, opts ...grpc.CallOption) (*SampleResponse, error) {
	out := new(SampleResponse)
	err := c.cc.Invoke(ctx, "/importer.Importer/SampleRPC", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ImporterServer is the server API for Importer service.
type ImporterServer interface {
	SampleRPC(context.Context, *SampleRequest) (*SampleResponse, error)
}

// UnimplementedImporterServer can be embedded to have forward compatible implementations.
type UnimplementedImporterServer struct {
}

func (*UnimplementedImporterServer) SampleRPC(ctx context.Context, req *SampleRequest) (*SampleResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SampleRPC not implemented")
}

func RegisterImporterServer(s *grpc.Server, srv ImporterServer) {
	s.RegisterService(&_Importer_serviceDesc, srv)
}

func _Importer_SampleRPC_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(SampleRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ImporterServer).SampleRPC(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/importer.Importer/SampleRPC",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ImporterServer).SampleRPC(ctx, req.(*SampleRequest))
	}
	return interceptor(ctx, in, info, handler)
}

var _Importer_serviceDesc = grpc.ServiceDesc{
	ServiceName: "importer.Importer",
	HandlerType: (*ImporterServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "SampleRPC",
			Handler:    _Importer_SampleRPC_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "importer.proto",
}

func (m *SampleRequest) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalToSizedBuffer(dAtA[:size])
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *SampleRequest) MarshalTo(dAtA []byte) (int, error) {
	size := m.Size()
	return m.MarshalToSizedBuffer(dAtA[:size])
}

func (m *SampleRequest) MarshalToSizedBuffer(dAtA []byte) (int, error) {
	i := len(dAtA)
	_ = i
	var l int
	_ = l
	if len(m.SampleField) > 0 {
		i -= len(m.SampleField)
		copy(dAtA[i:], m.SampleField)
		i = encodeVarintImporter(dAtA, i, uint64(len(m.SampleField)))
		i--
		dAtA[i] = 0xa
	}
	return len(dAtA) - i, nil
}

func (m *SampleResponse) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalToSizedBuffer(dAtA[:size])
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *SampleResponse) MarshalTo(dAtA []byte) (int, error) {
	size := m.Size()
	return m.MarshalToSizedBuffer(dAtA[:size])
}

func (m *SampleResponse) MarshalToSizedBuffer(dAtA []byte) (int, error) {
	i := len(dAtA)
	_ = i
	var l int
	_ = l
	if len(m.SampleField) > 0 {
		i -= len(m.SampleField)
		copy(dAtA[i:], m.SampleField)
		i = encodeVarintImporter(dAtA, i, uint64(len(m.SampleField)))
		i--
		dAtA[i] = 0xa
	}
	return len(dAtA) - i, nil
}

func encodeVarintImporter(dAtA []byte, offset int, v uint64) int {
	offset -= sovImporter(v)
	base := offset
	for v >= 1<<7 {
		dAtA[offset] = uint8(v&0x7f | 0x80)
		v >>= 7
		offset++
	}
	dAtA[offset] = uint8(v)
	return base
}
func (m *SampleRequest) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	l = len(m.SampleField)
	if l > 0 {
		n += 1 + l + sovImporter(uint64(l))
	}
	return n
}

func (m *SampleResponse) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	l = len(m.SampleField)
	if l > 0 {
		n += 1 + l + sovImporter(uint64(l))
	}
	return n
}

func sovImporter(x uint64) (n int) {
	return (math_bits.Len64(x|1) + 6) / 7
}
func sozImporter(x uint64) (n int) {
	return sovImporter(uint64((x << 1) ^ uint64((int64(x) >> 63))))
}
func (this *SampleRequest) String() string {
	if this == nil {
		return "nil"
	}
	s := strings.Join([]string{`&SampleRequest{`,
		`SampleField:` + fmt.Sprintf("%v", this.SampleField) + `,`,
		`}`,
	}, "")
	return s
}
func (this *SampleResponse) String() string {
	if this == nil {
		return "nil"
	}
	s := strings.Join([]string{`&SampleResponse{`,
		`SampleField:` + fmt.Sprintf("%v", this.SampleField) + `,`,
		`}`,
	}, "")
	return s
}
func valueToStringImporter(v interface{}) string {
	rv := reflect.ValueOf(v)
	if rv.IsNil() {
		return "nil"
	}
	pv := reflect.Indirect(rv).Interface()
	return fmt.Sprintf("*%v", pv)
}
func (m *SampleRequest) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowImporter
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: SampleRequest: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: SampleRequest: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field SampleField", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowImporter
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthImporter
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthImporter
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.SampleField = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipImporter(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthImporter
			}
			if (iNdEx + skippy) < 0 {
				return ErrInvalidLengthImporter
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}

	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func (m *SampleResponse) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowImporter
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: SampleResponse: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: SampleResponse: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field SampleField", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowImporter
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthImporter
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthImporter
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.SampleField = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipImporter(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthImporter
			}
			if (iNdEx + skippy) < 0 {
				return ErrInvalidLengthImporter
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}

	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func skipImporter(dAtA []byte) (n int, err error) {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return 0, ErrIntOverflowImporter
			}
			if iNdEx >= l {
				return 0, io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= (uint64(b) & 0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		wireType := int(wire & 0x7)
		switch wireType {
		case 0:
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, ErrIntOverflowImporter
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				iNdEx++
				if dAtA[iNdEx-1] < 0x80 {
					break
				}
			}
			return iNdEx, nil
		case 1:
			iNdEx += 8
			return iNdEx, nil
		case 2:
			var length int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, ErrIntOverflowImporter
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				length |= (int(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if length < 0 {
				return 0, ErrInvalidLengthImporter
			}
			iNdEx += length
			if iNdEx < 0 {
				return 0, ErrInvalidLengthImporter
			}
			return iNdEx, nil
		case 3:
			for {
				var innerWire uint64
				var start int = iNdEx
				for shift := uint(0); ; shift += 7 {
					if shift >= 64 {
						return 0, ErrIntOverflowImporter
					}
					if iNdEx >= l {
						return 0, io.ErrUnexpectedEOF
					}
					b := dAtA[iNdEx]
					iNdEx++
					innerWire |= (uint64(b) & 0x7F) << shift
					if b < 0x80 {
						break
					}
				}
				innerWireType := int(innerWire & 0x7)
				if innerWireType == 4 {
					break
				}
				next, err := skipImporter(dAtA[start:])
				if err != nil {
					return 0, err
				}
				iNdEx = start + next
				if iNdEx < 0 {
					return 0, ErrInvalidLengthImporter
				}
			}
			return iNdEx, nil
		case 4:
			return iNdEx, nil
		case 5:
			iNdEx += 4
			return iNdEx, nil
		default:
			return 0, fmt.Errorf("proto: illegal wireType %d", wireType)
		}
	}
	panic("unreachable")
}

var (
	ErrInvalidLengthImporter = fmt.Errorf("proto: negative length found during unmarshaling")
	ErrIntOverflowImporter   = fmt.Errorf("proto: integer overflow")
)
