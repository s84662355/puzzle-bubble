package bizstub

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

type Server struct {
	name       string
	listen     string
	fullMethod string
	logger     *slog.Logger
	grpcSrv    *grpc.Server
	listener   net.Listener
}

func NewServer(name, listenAddr, fullMethod string, logger *slog.Logger) *Server {
	s := &Server{
		name:       name,
		listen:     listenAddr,
		fullMethod: fullMethod,
		logger:     logger,
		grpcSrv:    grpc.NewServer(),
	}
	s.register()
	return s
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	s.listener = ln
	s.logger.Info("service started", "service", s.name, "listen", s.listen)
	return s.grpcSrv.Serve(ln)
}

func (s *Server) Stop(ctx context.Context) error {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	done := make(chan struct{})
	go func() {
		s.grpcSrv.GracefulStop()
		close(done)
	}()
	select {
	case <-ctx.Done():
		s.grpcSrv.Stop()
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *Server) register() {
	api := &svcAPI{server: s}
	s.grpcSrv.RegisterService(&grpc.ServiceDesc{
		ServiceName: s.name,
		HandlerType: (*gatewayServiceAPI)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "HandleGatewayMessage",
				Handler:    api.handleGatewayMessageHandler,
			},
		},
		Streams:  []grpc.StreamDesc{},
		Metadata: "api/" + s.name + ".proto",
	}, api)
}

type gatewayServiceAPI interface {
	HandleGatewayMessage(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

type svcAPI struct {
	server *Server
}

func (s *svcAPI) HandleGatewayMessage(_ context.Context, in *structpb.Struct) (*structpb.Struct, error) {
	if in == nil {
		return nil, errors.New("nil request")
	}
	req := in.AsMap()
	playerID, _ := req["player_id"].(string)
	msgID, _ := req["msg_id"].(float64)
	body, _ := req["body"].(map[string]any)

	out, _ := structpb.NewStruct(map[string]any{
		"code": 0,
		"msg":  "ok",
		"body": map[string]any{
			"service":   s.server.name,
			"player_id": playerID,
			"msg_id":    msgID,
			"echo":      body,
			"ts":        time.Now().UTC().Format(time.RFC3339),
		},
	})
	return out, nil
}

func (s *svcAPI) handleGatewayMessageHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := &structpb.Struct{}
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(gatewayServiceAPI).HandleGatewayMessage(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: s.server.fullMethod,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(gatewayServiceAPI).HandleGatewayMessage(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}
