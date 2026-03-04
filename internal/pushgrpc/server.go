package pushgrpc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"time"

	"game-gateway/internal/config"
	"game-gateway/internal/gateway"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

type Server struct {
	cfg        config.Config
	logger     *slog.Logger
	gw         *gateway.TCPGateway
	grpcServer *grpc.Server
	listener   net.Listener
}

func NewServer(cfg config.Config, logger *slog.Logger, gw *gateway.TCPGateway) (*Server, error) {
	if gw == nil {
		return nil, errors.New("nil gateway")
	}
	s := &Server{
		cfg:    cfg,
		logger: logger,
		gw:     gw,
	}
	s.grpcServer = grpc.NewServer()
	registerPushService(s.grpcServer, &pushService{server: s})
	return s, nil
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.GRPCListenAddr)
	if err != nil {
		return err
	}
	s.listener = ln
	return s.grpcServer.Serve(ln)
}

func (s *Server) Stop(ctx context.Context) error {
	if s.listener != nil {
		_ = s.listener.Close()
	}

	done := make(chan struct{})
	go func() {
		s.grpcServer.GracefulStop()
		close(done)
	}()

	select {
	case <-ctx.Done():
		s.grpcServer.Stop()
		return ctx.Err()
	case <-done:
		return nil
	}
}

type pushService struct {
	server *Server
}

func (p *pushService) PushToPlayer(_ context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	playerID := getString(req, "player_id")
	cmd, err := getUint16(req, "cmd")
	if err != nil {
		return mapResp(400, err.Error(), map[string]any{}), nil
	}
	payload, err := getPayloadJSON(req, "payload")
	if err != nil {
		return mapResp(400, err.Error(), map[string]any{}), nil
	}

	n, pushErr := p.server.gw.PushToPlayer(playerID, cmd, payload)
	if pushErr != nil {
		return mapResp(500, pushErr.Error(), map[string]any{"pushed": n}), nil
	}

	return mapResp(0, "ok", map[string]any{
		"pushed": n,
	}), nil
}

func (p *pushService) PushToSession(_ context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	sessionID, err := getUint64(req, "session_id")
	if err != nil {
		return mapResp(400, err.Error(), map[string]any{}), nil
	}
	cmd, err := getUint16(req, "cmd")
	if err != nil {
		return mapResp(400, err.Error(), map[string]any{}), nil
	}
	payload, err := getPayloadJSON(req, "payload")
	if err != nil {
		return mapResp(400, err.Error(), map[string]any{}), nil
	}

	if err := p.server.gw.PushToSession(sessionID, cmd, payload); err != nil {
		return mapResp(404, err.Error(), map[string]any{}), nil
	}
	return mapResp(0, "ok", map[string]any{}), nil
}

func (p *pushService) PushToAll(_ context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	cmd, err := getUint16(req, "cmd")
	if err != nil {
		return mapResp(400, err.Error(), map[string]any{}), nil
	}
	payload, err := getPayloadJSON(req, "payload")
	if err != nil {
		return mapResp(400, err.Error(), map[string]any{}), nil
	}
	n, pushErr := p.server.gw.PushToAll(cmd, payload)
	if pushErr != nil {
		return mapResp(500, pushErr.Error(), map[string]any{"pushed": n}), nil
	}
	return mapResp(0, "ok", map[string]any{
		"pushed": n,
	}), nil
}

func (p *pushService) GetNodeInfo(_ context.Context, _ *structpb.Struct) (*structpb.Struct, error) {
	info := p.server.gw.SnapshotNodeInfo()
	info["grpc_listen"] = p.server.cfg.GRPCListenAddr
	info["server_time"] = time.Now().UTC().Format(time.RFC3339)
	return mapResp(0, "ok", info), nil
}

type pushServiceAPI interface {
	PushToPlayer(context.Context, *structpb.Struct) (*structpb.Struct, error)
	PushToSession(context.Context, *structpb.Struct) (*structpb.Struct, error)
	PushToAll(context.Context, *structpb.Struct) (*structpb.Struct, error)
	GetNodeInfo(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

func registerPushService(s *grpc.Server, srv pushServiceAPI) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "gateway.GatewayPushService",
		HandlerType: (*pushServiceAPI)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "PushToPlayer",
				Handler:    pushToPlayerHandler,
			},
			{
				MethodName: "PushToSession",
				Handler:    pushToSessionHandler,
			},
			{
				MethodName: "PushToAll",
				Handler:    pushToAllHandler,
			},
			{
				MethodName: "GetNodeInfo",
				Handler:    getNodeInfoHandler,
			},
		},
		Streams:  []grpc.StreamDesc{},
		Metadata: "api/push.proto",
	}, srv)
}

func pushToPlayerHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := &structpb.Struct{}
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(pushServiceAPI).PushToPlayer(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/gateway.GatewayPushService/PushToPlayer",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(pushServiceAPI).PushToPlayer(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func pushToSessionHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := &structpb.Struct{}
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(pushServiceAPI).PushToSession(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/gateway.GatewayPushService/PushToSession",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(pushServiceAPI).PushToSession(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func pushToAllHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := &structpb.Struct{}
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(pushServiceAPI).PushToAll(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/gateway.GatewayPushService/PushToAll",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(pushServiceAPI).PushToAll(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func getNodeInfoHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := &structpb.Struct{}
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(pushServiceAPI).GetNodeInfo(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/gateway.GatewayPushService/GetNodeInfo",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(pushServiceAPI).GetNodeInfo(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func getString(s *structpb.Struct, key string) string {
	if s == nil {
		return ""
	}
	v, ok := s.AsMap()[key]
	if !ok {
		return ""
	}
	out, _ := v.(string)
	return out
}

func getUint16(s *structpb.Struct, key string) (uint16, error) {
	if s == nil {
		return 0, errors.New("nil request")
	}
	v, ok := s.AsMap()[key]
	if !ok {
		return 0, errors.New("missing " + key)
	}
	n, ok := v.(float64)
	if !ok || n < 0 || n > 65535 {
		return 0, errors.New("invalid " + key)
	}
	return uint16(n), nil
}

func getUint64(s *structpb.Struct, key string) (uint64, error) {
	if s == nil {
		return 0, errors.New("nil request")
	}
	v, ok := s.AsMap()[key]
	if !ok {
		return 0, errors.New("missing " + key)
	}
	n, ok := v.(float64)
	if !ok || n < 0 {
		return 0, errors.New("invalid " + key)
	}
	return uint64(n), nil
}

func getPayloadJSON(s *structpb.Struct, key string) ([]byte, error) {
	if s == nil {
		return []byte(`{}`), nil
	}
	v, ok := s.AsMap()[key]
	if !ok {
		return []byte(`{}`), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func mapResp(code int, msg string, body map[string]any) *structpb.Struct {
	resp := map[string]any{
		"code": code,
		"msg":  msg,
		"body": body,
	}
	s, err := structpb.NewStruct(resp)
	if err != nil {
		return &structpb.Struct{}
	}
	return s
}
