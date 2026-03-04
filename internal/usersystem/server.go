package usersystem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

type Config struct {
	HTTPListenAddr  string
	GRPCListenAddr  string
	TokenTTL        time.Duration
	ShutdownTimeout time.Duration
	RedisAddr       string
	RedisPassword   string
	RedisDB         int
	RedisKeyPrefix  string
}

type Server struct {
	cfg    Config
	logger *slog.Logger

	rdb *redis.Client

	httpSrv *http.Server
	grpcSrv *grpc.Server
	httpLn  net.Listener
	grpcLn  net.Listener
}

func NewServer(cfg Config, logger *slog.Logger) (*Server, error) {
	if cfg.RedisAddr == "" {
		return nil, errors.New("empty redis addr")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, err
	}

	s := &Server{
		cfg:    cfg,
		logger: logger,
		rdb:    rdb,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/healthz", s.handleHealthz)
	s.httpSrv = &http.Server{
		Addr:    cfg.HTTPListenAddr,
		Handler: mux,
	}

	s.grpcSrv = grpc.NewServer()
	registerUserService(s.grpcSrv, &userService{server: s})
	return s, nil
}

func (s *Server) StartHTTP() error {
	ln, err := net.Listen("tcp", s.cfg.HTTPListenAddr)
	if err != nil {
		return err
	}
	s.httpLn = ln
	s.logger.Info("user http server started", "addr", s.cfg.HTTPListenAddr)
	return s.httpSrv.Serve(ln)
}

func (s *Server) StartGRPC() error {
	ln, err := net.Listen("tcp", s.cfg.GRPCListenAddr)
	if err != nil {
		return err
	}
	s.grpcLn = ln
	s.logger.Info("user grpc server started", "addr", s.cfg.GRPCListenAddr)
	return s.grpcSrv.Serve(ln)
}

func (s *Server) Stop(ctx context.Context) error {
	if s.httpLn != nil {
		_ = s.httpLn.Close()
	}
	if s.grpcLn != nil {
		_ = s.grpcLn.Close()
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- s.httpSrv.Shutdown(ctx)
	}()
	go func() {
		done := make(chan struct{})
		go func() {
			s.grpcSrv.GracefulStop()
			close(done)
		}()
		select {
		case <-ctx.Done():
			s.grpcSrv.Stop()
			errCh <- ctx.Err()
		case <-done:
			errCh <- nil
		}
	}()

	var firstErr error
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) && firstErr == nil {
			firstErr = err
		}
	}
	_ = s.rdb.Close()
	return firstErr
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type loginReq struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	type loginResp struct {
		Token     string `json:"token"`
		PlayerID  string `json:"player_id"`
		ExpiresIn int64  `json:"expires_in"`
	}
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Password == "" {
		http.Error(w, "missing username or password", http.StatusBadRequest)
		return
	}

	// Placeholder auth rule: accept any non-empty username/password.
	token, err := generateToken()
	if err != nil {
		http.Error(w, "generate token failed", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.rdb.Set(ctx, s.tokenKey(token), req.Username, s.cfg.TokenTTL).Err(); err != nil {
		http.Error(w, "store token failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(loginResp{
		Token:     token,
		PlayerID:  req.Username,
		ExpiresIn: int64(s.cfg.TokenTTL.Seconds()),
	})
}

func generateToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Server) validateToken(ctx context.Context, token string) (string, bool, string) {
	if token == "" {
		return "", false, "missing_token"
	}
	val, err := s.rdb.Get(ctx, s.tokenKey(token)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, "token_not_found"
	}
	if err != nil {
		s.logger.Error("redis get token failed", "error", err)
		return "", false, "redis_error"
	}
	return val, true, "ok"
}

func (s *Server) refreshToken(ctx context.Context, token string) (bool, string) {
	if token == "" {
		return false, "missing_token"
	}
	key := s.tokenKey(token)
	ok, err := s.rdb.Expire(ctx, key, s.cfg.TokenTTL).Result()
	if err != nil {
		s.logger.Error("redis expire token failed", "error", err)
		return false, "redis_error"
	}
	if !ok {
		return false, "token_not_found"
	}
	return true, "ok"
}

func (s *Server) tokenKey(token string) string {
	prefix := s.cfg.RedisKeyPrefix
	if prefix == "" {
		prefix = "game:token:"
	}
	return prefix + token
}

type userService struct {
	server *Server
}

func (u *userService) Login(ctx context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	username := getString(req, "username")
	password := getString(req, "password")
	if username == "" || password == "" {
		out, _ := structpb.NewStruct(map[string]any{
			"ok":     false,
			"reason": "missing_username_or_password",
		})
		return out, nil
	}
	token, err := generateToken()
	if err != nil {
		return nil, err
	}
	if err := u.server.rdb.Set(ctx, u.server.tokenKey(token), username, u.server.cfg.TokenTTL).Err(); err != nil {
		return nil, err
	}
	out, _ := structpb.NewStruct(map[string]any{
		"ok":         true,
		"token":      token,
		"player_id":  username,
		"expires_in": int64(u.server.cfg.TokenTTL.Seconds()),
	})
	return out, nil
}

func (u *userService) ValidateToken(ctx context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	token := getString(req, "token")
	playerID, valid, reason := u.server.validateToken(ctx, token)
	out, _ := structpb.NewStruct(map[string]any{
		"valid":   valid,
		"user_id": playerID,
		"reason":  reason,
	})
	return out, nil
}

func (u *userService) RefreshToken(ctx context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	token := getString(req, "token")
	valid, reason := u.server.refreshToken(ctx, token)
	out, _ := structpb.NewStruct(map[string]any{
		"valid":  valid,
		"reason": reason,
	})
	return out, nil
}

type userServiceAPI interface {
	Login(context.Context, *structpb.Struct) (*structpb.Struct, error)
	ValidateToken(context.Context, *structpb.Struct) (*structpb.Struct, error)
	RefreshToken(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

func registerUserService(s *grpc.Server, srv userServiceAPI) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "auth.AuthService",
		HandlerType: (*userServiceAPI)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Login",
				Handler:    loginHandler,
			},
			{
				MethodName: "ValidateToken",
				Handler:    validateTokenHandler,
			},
			{
				MethodName: "RefreshToken",
				Handler:    refreshTokenHandler,
			},
		},
		Streams:  []grpc.StreamDesc{},
		Metadata: "api/auth.proto",
	}, srv)
}

func loginHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := &structpb.Struct{}
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(userServiceAPI).Login(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/auth.AuthService/Login",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(userServiceAPI).Login(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func validateTokenHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := &structpb.Struct{}
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(userServiceAPI).ValidateToken(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/auth.AuthService/ValidateToken",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(userServiceAPI).ValidateToken(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func refreshTokenHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := &structpb.Struct{}
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(userServiceAPI).RefreshToken(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/auth.AuthService/RefreshToken",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(userServiceAPI).RefreshToken(ctx, req.(*structpb.Struct))
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
