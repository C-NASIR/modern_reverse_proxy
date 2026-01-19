package plugin

import (
	"context"

	"modern_reverse_proxy/internal/plugin/proto"

	"google.golang.org/grpc"
)

type Client struct {
	conn *grpc.ClientConn
	stub pluginpb.FilterServiceClient
}

func NewClient(conn *grpc.ClientConn) *Client {
	return &Client{conn: conn, stub: pluginpb.NewFilterServiceClient(conn)}
}

func (c *Client) ApplyRequest(ctx context.Context, req *pluginpb.ApplyRequestRequest) (*pluginpb.ApplyRequestResponse, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	return c.stub.ApplyRequest(ctx, req)
}

func (c *Client) ApplyResponse(ctx context.Context, req *pluginpb.ApplyResponseRequest) (*pluginpb.ApplyResponseResponse, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	return c.stub.ApplyResponse(ctx, req)
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
