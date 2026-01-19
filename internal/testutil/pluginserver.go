package testutil

import (
	"context"
	"net"
	"testing"

	"modern_reverse_proxy/internal/plugin/proto"

	"google.golang.org/grpc"
)

type PluginHandlers struct {
	ApplyRequest  func(context.Context, *pluginpb.ApplyRequestRequest) (*pluginpb.ApplyRequestResponse, error)
	ApplyResponse func(context.Context, *pluginpb.ApplyResponseRequest) (*pluginpb.ApplyResponseResponse, error)
}

func StartPluginServer(t *testing.T, handlers PluginHandlers) (string, func()) {
	t.Helper()
	if handlers.ApplyRequest == nil {
		handlers.ApplyRequest = func(context.Context, *pluginpb.ApplyRequestRequest) (*pluginpb.ApplyRequestResponse, error) {
			return &pluginpb.ApplyRequestResponse{Action: pluginpb.ApplyRequestResponse_CONTINUE}, nil
		}
	}
	if handlers.ApplyResponse == nil {
		handlers.ApplyResponse = func(context.Context, *pluginpb.ApplyResponseRequest) (*pluginpb.ApplyResponseResponse, error) {
			return &pluginpb.ApplyResponseResponse{}, nil
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := grpc.NewServer()
	pluginpb.RegisterFilterServiceServer(server, &pluginServer{handlers: handlers})
	go func() {
		_ = server.Serve(ln)
	}()

	closeFn := func() {
		server.GracefulStop()
		_ = ln.Close()
	}

	return ln.Addr().String(), closeFn
}

type pluginServer struct {
	pluginpb.UnimplementedFilterServiceServer
	handlers PluginHandlers
}

func (p *pluginServer) ApplyRequest(ctx context.Context, req *pluginpb.ApplyRequestRequest) (*pluginpb.ApplyRequestResponse, error) {
	return p.handlers.ApplyRequest(ctx, req)
}

func (p *pluginServer) ApplyResponse(ctx context.Context, req *pluginpb.ApplyResponseRequest) (*pluginpb.ApplyResponseResponse, error) {
	return p.handlers.ApplyResponse(ctx, req)
}
