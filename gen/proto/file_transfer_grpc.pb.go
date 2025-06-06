// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.5.1
// - protoc             (unknown)
// source: proto/file_transfer.proto

package proto

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.64.0 or later.
const _ = grpc.SupportPackageIsVersion9

const (
	FileTransferService_GetFS_FullMethodName               = "/control_grpc.FileTransferService/GetFS"
	FileTransferService_DownloadFile_FullMethodName        = "/control_grpc.FileTransferService/DownloadFile"
	FileTransferService_DownloadFolderAsZip_FullMethodName = "/control_grpc.FileTransferService/DownloadFolderAsZip"
)

// FileTransferServiceClient is the client API for FileTransferService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type FileTransferServiceClient interface {
	// Request content for a specific path. Empty path means roots.
	GetFS(ctx context.Context, in *FSRequest, opts ...grpc.CallOption) (*FSResponse, error)
	// Download a file from the server.
	DownloadFile(ctx context.Context, in *FileRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[FileChunk], error)
	// Download a folder from the server as a zip archive.
	DownloadFolderAsZip(ctx context.Context, in *FileRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[FileChunk], error)
}

type fileTransferServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewFileTransferServiceClient(cc grpc.ClientConnInterface) FileTransferServiceClient {
	return &fileTransferServiceClient{cc}
}

func (c *fileTransferServiceClient) GetFS(ctx context.Context, in *FSRequest, opts ...grpc.CallOption) (*FSResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(FSResponse)
	err := c.cc.Invoke(ctx, FileTransferService_GetFS_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *fileTransferServiceClient) DownloadFile(ctx context.Context, in *FileRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[FileChunk], error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	stream, err := c.cc.NewStream(ctx, &FileTransferService_ServiceDesc.Streams[0], FileTransferService_DownloadFile_FullMethodName, cOpts...)
	if err != nil {
		return nil, err
	}
	x := &grpc.GenericClientStream[FileRequest, FileChunk]{ClientStream: stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

// This type alias is provided for backwards compatibility with existing code that references the prior non-generic stream type by name.
type FileTransferService_DownloadFileClient = grpc.ServerStreamingClient[FileChunk]

func (c *fileTransferServiceClient) DownloadFolderAsZip(ctx context.Context, in *FileRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[FileChunk], error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	stream, err := c.cc.NewStream(ctx, &FileTransferService_ServiceDesc.Streams[1], FileTransferService_DownloadFolderAsZip_FullMethodName, cOpts...)
	if err != nil {
		return nil, err
	}
	x := &grpc.GenericClientStream[FileRequest, FileChunk]{ClientStream: stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

// This type alias is provided for backwards compatibility with existing code that references the prior non-generic stream type by name.
type FileTransferService_DownloadFolderAsZipClient = grpc.ServerStreamingClient[FileChunk]

// FileTransferServiceServer is the server API for FileTransferService service.
// All implementations should embed UnimplementedFileTransferServiceServer
// for forward compatibility.
type FileTransferServiceServer interface {
	// Request content for a specific path. Empty path means roots.
	GetFS(context.Context, *FSRequest) (*FSResponse, error)
	// Download a file from the server.
	DownloadFile(*FileRequest, grpc.ServerStreamingServer[FileChunk]) error
	// Download a folder from the server as a zip archive.
	DownloadFolderAsZip(*FileRequest, grpc.ServerStreamingServer[FileChunk]) error
}

// UnimplementedFileTransferServiceServer should be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedFileTransferServiceServer struct{}

func (UnimplementedFileTransferServiceServer) GetFS(context.Context, *FSRequest) (*FSResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetFS not implemented")
}
func (UnimplementedFileTransferServiceServer) DownloadFile(*FileRequest, grpc.ServerStreamingServer[FileChunk]) error {
	return status.Errorf(codes.Unimplemented, "method DownloadFile not implemented")
}
func (UnimplementedFileTransferServiceServer) DownloadFolderAsZip(*FileRequest, grpc.ServerStreamingServer[FileChunk]) error {
	return status.Errorf(codes.Unimplemented, "method DownloadFolderAsZip not implemented")
}
func (UnimplementedFileTransferServiceServer) testEmbeddedByValue() {}

// UnsafeFileTransferServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to FileTransferServiceServer will
// result in compilation errors.
type UnsafeFileTransferServiceServer interface {
	mustEmbedUnimplementedFileTransferServiceServer()
}

func RegisterFileTransferServiceServer(s grpc.ServiceRegistrar, srv FileTransferServiceServer) {
	// If the following call pancis, it indicates UnimplementedFileTransferServiceServer was
	// embedded by pointer and is nil.  This will cause panics if an
	// unimplemented method is ever invoked, so we test this at initialization
	// time to prevent it from happening at runtime later due to I/O.
	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&FileTransferService_ServiceDesc, srv)
}

func _FileTransferService_GetFS_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(FSRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(FileTransferServiceServer).GetFS(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: FileTransferService_GetFS_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(FileTransferServiceServer).GetFS(ctx, req.(*FSRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _FileTransferService_DownloadFile_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(FileRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(FileTransferServiceServer).DownloadFile(m, &grpc.GenericServerStream[FileRequest, FileChunk]{ServerStream: stream})
}

// This type alias is provided for backwards compatibility with existing code that references the prior non-generic stream type by name.
type FileTransferService_DownloadFileServer = grpc.ServerStreamingServer[FileChunk]

func _FileTransferService_DownloadFolderAsZip_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(FileRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(FileTransferServiceServer).DownloadFolderAsZip(m, &grpc.GenericServerStream[FileRequest, FileChunk]{ServerStream: stream})
}

// This type alias is provided for backwards compatibility with existing code that references the prior non-generic stream type by name.
type FileTransferService_DownloadFolderAsZipServer = grpc.ServerStreamingServer[FileChunk]

// FileTransferService_ServiceDesc is the grpc.ServiceDesc for FileTransferService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var FileTransferService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "control_grpc.FileTransferService",
	HandlerType: (*FileTransferServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "GetFS",
			Handler:    _FileTransferService_GetFS_Handler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "DownloadFile",
			Handler:       _FileTransferService_DownloadFile_Handler,
			ServerStreams: true,
		},
		{
			StreamName:    "DownloadFolderAsZip",
			Handler:       _FileTransferService_DownloadFolderAsZip_Handler,
			ServerStreams: true,
		},
	},
	Metadata: "proto/file_transfer.proto",
}
