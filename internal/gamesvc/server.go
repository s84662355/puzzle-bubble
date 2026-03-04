package gamesvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"game-gateway/internal/gameplay"
	"game-gateway/internal/protocol"
	"game-gateway/internal/room"
	"game-gateway/internal/userclient"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

type Config struct {
	WSListenAddr     string
	GRPCListenAddr   string
	PublicWSAddr     string
	AuthGRPCAddr     string
	TokenVerifyTimeout time.Duration
	HeartbeatTimeout time.Duration
	RoomFrameTick    time.Duration
}

type Session struct {
	RoomID         string
	Players        map[string]struct{}
	Tickets        map[string]string // player -> ticket
	States         map[string]*gameplay.PlayerState
	WinnerID       string
	LastHeartbeat  map[string]int64 // player -> unix milli
	Finalized      bool
}

type Server struct {
	cfg    Config
	logger *slog.Logger

	httpSrv *http.Server
	grpcSrv *grpc.Server
	httpLn  net.Listener
	grpcLn  net.Listener

	mu       sync.Mutex
	sessions map[string]*Session
	wsConns  map[string]map[string]*websocket.Conn
	roomMgr  *room.Manager

	roomMu      sync.Mutex
	roomWS      map[string]*websocket.Conn // playerID -> conn
	roomLastHB  map[string]int64           // playerID -> unix milli
	roomOfPlayer map[string]string         // playerID -> roomID
	roomMonitor map[string]bool            // roomID -> monitor running
	authVerifier *userclient.GRPCVerifier
}

func New(cfg Config, logger *slog.Logger) *Server {
	if cfg.AuthGRPCAddr == "" {
		cfg.AuthGRPCAddr = "127.0.0.1:19090"
	}
	if cfg.TokenVerifyTimeout <= 0 {
		cfg.TokenVerifyTimeout = 2 * time.Second
	}
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = 5 * time.Second
	}
	verifier, err := userclient.NewGRPCVerifier(cfg.AuthGRPCAddr)
	if err != nil {
		panic(err)
	}
	s := &Server{
		cfg:      cfg,
		logger:   logger,
		sessions: make(map[string]*Session),
		wsConns:  make(map[string]map[string]*websocket.Conn),
		roomMgr:  room.NewManager(cfg.RoomFrameTick),
		roomWS:   make(map[string]*websocket.Conn),
		roomLastHB: make(map[string]int64),
		roomOfPlayer: make(map[string]string),
		roomMonitor: make(map[string]bool),
		authVerifier: verifier,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/game", s.handleWSGame)
	mux.HandleFunc("/ws/room", s.handleWSRoom)
	mux.HandleFunc("/healthz", s.handleHealthz)
	s.httpSrv = &http.Server{Addr: cfg.WSListenAddr, Handler: mux}

	s.grpcSrv = grpc.NewServer()
	registerGameControlService(s.grpcSrv, &gameControl{server: s})
	registerRoomService(s.grpcSrv, &roomGatewayService{server: s})
	return s
}

func (s *Server) StartWS() error {
	ln, err := net.Listen("tcp", s.cfg.WSListenAddr)
	if err != nil {
		return err
	}
	s.httpLn = ln
	s.logger.Info("game ws started", "addr", s.cfg.WSListenAddr)
	return s.httpSrv.Serve(ln)
}

func (s *Server) StartGRPC() error {
	ln, err := net.Listen("tcp", s.cfg.GRPCListenAddr)
	if err != nil {
		return err
	}
	s.grpcLn = ln
	s.logger.Info("game grpc started", "addr", s.cfg.GRPCListenAddr)
	return s.grpcSrv.Serve(ln)
}

func (s *Server) Stop(ctx context.Context) error {
	if s.httpLn != nil {
		_ = s.httpLn.Close()
	}
	if s.grpcLn != nil {
		_ = s.grpcLn.Close()
	}
	_ = s.httpSrv.Shutdown(ctx)
	done := make(chan struct{})
	go func() {
		s.grpcSrv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		s.grpcSrv.Stop()
	}
	if s.authVerifier != nil {
		_ = s.authVerifier.Close()
	}
	return nil
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

var wsUpgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func (s *Server) handleWSGame(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	playerID := strings.TrimSpace(r.URL.Query().Get("player_id"))
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if roomID == "" || playerID == "" || ticket == "" || token == "" {
		http.Error(w, "missing room_id/player_id/ticket/token", http.StatusBadRequest)
		return
	}
	if !s.verifyToken(token, playerID) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.mu.Lock()
	sess, ok := s.sessions[roomID]
	if !ok || sess.Tickets[playerID] != ticket {
		s.mu.Unlock()
		_ = conn.WriteJSON(map[string]any{"type": "error", "msg": "invalid session or ticket"})
		_ = conn.Close()
		return
	}
	if _, ok := s.wsConns[roomID]; !ok {
		s.wsConns[roomID] = make(map[string]*websocket.Conn)
	}
	if old := s.wsConns[roomID][playerID]; old != nil {
		_ = old.Close()
	}
	s.wsConns[roomID][playerID] = conn
	now := time.Now().UnixMilli()
	sess.LastHeartbeat[playerID] = now
	s.mu.Unlock()
	s.roomMu.Lock()
	s.roomOfPlayer[playerID] = roomID
	s.roomLastHB[playerID] = now
	s.ensureRoomMonitorLocked(roomID)
	s.roomMu.Unlock()

	defer func() {
		s.mu.Lock()
		if m, ok := s.wsConns[roomID]; ok {
			delete(m, playerID)
			if len(m) == 0 {
				delete(s.wsConns, roomID)
			}
		}
		s.mu.Unlock()
		_ = conn.Close()
	}()

	done := make(chan struct{})
	go s.readWS(conn, roomID, playerID, token, done)
	ticker := time.NewTicker(70 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			payload, ok := s.buildState(roomID, playerID)
			if !ok {
				_ = conn.WriteJSON(map[string]any{"type": "error", "msg": "session closed"})
				return
			}
			if err := conn.WriteJSON(payload); err != nil {
				return
			}
		}
	}
}

func (s *Server) readWS(conn *websocket.Conn, roomID, playerID, token string, done chan struct{}) {
	defer close(done)
	for {
		var req map[string]any
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		typ, _ := req["type"].(string)
		now := time.Now().UnixMilli()
		switch typ {
		case "shot":
			angle, _ := req["angle"].(float64)
			s.mu.Lock()
			if sess := s.sessions[roomID]; sess != nil {
				sess.LastHeartbeat[playerID] = now
				_ = gameplay.BeginClientShot(sess.States[playerID], angle, time.Now().UnixMilli())
			}
			s.mu.Unlock()
		case "land":
			angle, _ := req["angle"].(float64)
			x, _ := req["x"].(float64)
			y, _ := req["y"].(float64)
			s.mu.Lock()
			if sess := s.sessions[roomID]; sess != nil {
				sess.LastHeartbeat[playerID] = now
				_ = gameplay.CommitLanding(sess.States[playerID], angle, x, y)
				s.resolveOutcomeLocked(sess)
			}
			s.mu.Unlock()
		case "aim":
			angle, _ := req["angle"].(float64)
			s.mu.Lock()
			if sess := s.sessions[roomID]; sess != nil {
				sess.LastHeartbeat[playerID] = now
				sess.States[playerID].AimAngle = gameplay.ClampAim(angle)
			}
			s.mu.Unlock()
		case "heartbeat":
			s.mu.Lock()
			if sess := s.sessions[roomID]; sess != nil {
				sess.LastHeartbeat[playerID] = now
			}
			s.mu.Unlock()
			if !s.refreshToken(token) {
				return
			}
		}
		s.roomMu.Lock()
		s.roomLastHB[playerID] = now
		s.roomOfPlayer[playerID] = roomID
		s.ensureRoomMonitorLocked(roomID)
		s.roomMu.Unlock()
	}
}

func (s *Server) buildState(roomID, playerID string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[roomID]
	if !ok {
		return nil, false
	}
	now := time.Now().UnixMilli()
	s.applyHeartbeatTimeoutsLocked(sess, now)
	for _, st := range sess.States {
		gameplay.TickState(st, now)
	}
	self := gameplay.CloneState(sess.States[playerID])
	others := make([]map[string]any, 0, len(sess.States)-1)
	for pid, st := range sess.States {
		if pid == playerID {
			continue
		}
		others = append(others, map[string]any{
			"player_id": pid,
			"state":     gameplay.CloneState(st),
		})
	}
	roomState := "playing"
	if sess.WinnerID != "" {
		roomState = "waiting"
	}
	return map[string]any{
		"type": "state",
		"room": map[string]any{
			"room_id":   roomID,
			"state":     roomState,
			"winner_id": sess.WinnerID,
		},
		"self":   self,
		"others": others,
	}, true
}

func (s *Server) resolveOutcomeLocked(sess *Session) {
	alive := make([]string, 0, len(sess.States))
	for pid, st := range sess.States {
		if st.Status != "lose" {
			alive = append(alive, pid)
		}
	}
	if len(sess.States) >= 2 && len(alive) == 1 {
		sess.WinnerID = alive[0]
		s.finalizeRoomAfterWinLocked(sess, alive[0])
	}
}

func (s *Server) finalizeRoomAfterWinLocked(sess *Session, winnerID string) {
	if sess == nil || winnerID == "" || sess.Finalized {
		return
	}
	// 同步房间成员：把非赢家移出房间。剩余玩家将自动成为房主（master）。
	for pid := range sess.States {
		if pid == winnerID {
			continue
		}
		_, _ = s.roomMgr.LeaveRoom(pid)
	}
	sess.Finalized = true
}

func (s *Server) applyHeartbeatTimeoutsLocked(sess *Session, nowMillis int64) {
	if sess == nil || sess.WinnerID != "" {
		return
	}
	if len(sess.LastHeartbeat) == 0 {
		return
	}
	timeoutMs := s.cfg.HeartbeatTimeout.Milliseconds()
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}
	for pid, hb := range sess.LastHeartbeat {
		if hb <= 0 {
			continue
		}
		if nowMillis-hb <= timeoutMs {
			continue
		}
		if st := sess.States[pid]; st != nil && st.Status != "lose" {
			st.Status = "lose"
		}
	}
	s.resolveOutcomeLocked(sess)
}

type gameControl struct {
	server *Server
}

func (g *gameControl) CreateSession(_ context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	if req == nil {
		return nil, errors.New("nil req")
	}
	m := req.AsMap()
	roomID, _ := m["room_id"].(string)
	if roomID == "" {
		return nil, errors.New("missing room_id")
	}
	rawPlayers, _ := m["players"].([]any)
	if len(rawPlayers) == 0 {
		return nil, errors.New("missing players")
	}
	players := make([]string, 0, len(rawPlayers))
	for _, v := range rawPlayers {
		if s, ok := v.(string); ok && s != "" {
			players = append(players, s)
		}
	}
	if len(players) == 0 {
		return nil, errors.New("empty players")
	}

	g.server.mu.Lock()
	defer g.server.mu.Unlock()
	sess := &Session{
		RoomID:        roomID,
		Players:       make(map[string]struct{}, len(players)),
		Tickets:       make(map[string]string, len(players)),
		States:        make(map[string]*gameplay.PlayerState, len(players)),
		LastHeartbeat: make(map[string]int64, len(players)),
	}
	tickets := make(map[string]any, len(players))
	now := time.Now().UnixMilli()
	for _, p := range players {
		sess.Players[p] = struct{}{}
		sess.States[p] = gameplay.NewInitialState()
		sess.LastHeartbeat[p] = now
		tk := randomTicket()
		sess.Tickets[p] = tk
		tickets[p] = tk
	}
	g.server.sessions[roomID] = sess

	out, _ := structpb.NewStruct(map[string]any{
		"room_id":   roomID,
		"game_addr": g.server.cfg.PublicWSAddr,
		"tickets":   tickets,
	})
	return out, nil
}

type gameControlServiceAPI interface {
	CreateSession(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

func registerGameControlService(s *grpc.Server, srv gameControlServiceAPI) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "game.GameControlService",
		HandlerType: (*gameControlServiceAPI)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "CreateSession", Handler: createSessionHandler},
		},
		Streams:  []grpc.StreamDesc{},
		Metadata: "api/game_control.proto",
	}, srv)
}

func createSessionHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := &structpb.Struct{}
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(gameControlServiceAPI).CreateSession(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/game.GameControlService/CreateSession"}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(gameControlServiceAPI).CreateSession(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func randomTicket() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type roomGatewayService struct {
	server *Server
}

func (r *roomGatewayService) HandleGatewayMessage(_ context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	if req == nil {
		return mapResp(100, "nil_request", nil), nil
	}
	m := req.AsMap()
	playerID, _ := m["player_id"].(string)
	msgID := toUint16(m["msg_id"])
	body := toMap(m["body"])
	if playerID == "" {
		return mapResp(100, "missing_player_id", nil), nil
	}

	switch msgID {
	case protocol.RoomMsgCreateRoom:
		name, _ := body["name"].(string)
		if name == "" {
			name = "Room-" + strconv.FormatInt(time.Now().Unix(), 10)
		}
		capacity := int(toFloat64(body["capacity"]))
		rm, err := r.server.roomMgr.CreateRoom(playerID, name, capacity)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		return mapResp(0, "ok", map[string]any{"room": roomToMap(rm)}), nil
	case protocol.RoomMsgJoinRoom:
		roomID, _ := body["room_id"].(string)
		rm, err := r.server.roomMgr.JoinRoom(playerID, roomID)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		return mapResp(0, "ok", map[string]any{"room": roomToMap(rm)}), nil
	case protocol.RoomMsgStartGame:
		rm, err := r.server.roomMgr.StartGame(playerID)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		return mapResp(0, "ok", map[string]any{"room": roomToMap(rm)}), nil
	case protocol.RoomMsgLeaveRoom:
		rm, err := r.server.roomMgr.LeaveRoom(playerID)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		r.server.roomMu.Lock()
		delete(r.server.roomOfPlayer, playerID)
		delete(r.server.roomLastHB, playerID)
		if c := r.server.roomWS[playerID]; c != nil {
			_ = c.Close()
		}
		delete(r.server.roomWS, playerID)
		r.server.roomMu.Unlock()
		return mapResp(0, "ok", map[string]any{"room": roomToMap(rm)}), nil
	case protocol.RoomMsgListRooms:
		items := r.server.roomMgr.ListRooms()
		rooms := make([]any, 0, len(items))
		for _, it := range items {
			rooms = append(rooms, roomToMap(it))
		}
		return mapResp(0, "ok", map[string]any{"rooms": rooms}), nil
	default:
		return mapResp(100, "unknown_msg_id", nil), nil
	}
}

type roomServiceAPI interface {
	HandleGatewayMessage(context.Context, *structpb.Struct) (*structpb.Struct, error)
}

func registerRoomService(s *grpc.Server, srv roomServiceAPI) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "room.RoomService",
		HandlerType: (*roomServiceAPI)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "HandleGatewayMessage", Handler: handleGatewayMessageHandler},
		},
		Streams:  []grpc.StreamDesc{},
		Metadata: "api/room.proto",
	}, srv)
}

func handleGatewayMessageHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := &structpb.Struct{}
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(roomServiceAPI).HandleGatewayMessage(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/room.RoomService/HandleGatewayMessage"}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(roomServiceAPI).HandleGatewayMessage(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func mapResp(code int, msg string, body map[string]any) *structpb.Struct {
	if body == nil {
		body = map[string]any{}
	}
	out, _ := structpb.NewStruct(map[string]any{
		"code": code,
		"msg":  msg,
		"body": body,
	})
	return out
}

func roomToMap(rm *room.Room) map[string]any {
	if rm == nil {
		return map[string]any{}
	}
	players := make([]any, 0, len(rm.Players))
	for _, p := range rm.Players {
		players = append(players, map[string]any{
			"player_id": p.PlayerID,
			"ready":     p.Ready,
		})
	}
	return map[string]any{
		"room_id":       rm.ID,
		"name":          rm.Name,
		"owner_id":      rm.OwnerID,
		"state":         string(rm.State),
		"max_players":   rm.MaxPlayers,
		"players":       players,
		"latest_cmd":    rm.CmdSeq,
		"playing_since": rm.PlayingSince.UnixMilli(),
	}
}

func toMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func toFloat64(v any) float64 {
	if n, ok := v.(float64); ok {
		return n
	}
	return 0
}

func toUint16(v any) uint16 {
	return uint16(toFloat64(v))
}

func (s *Server) handleWSRoom(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	playerID := strings.TrimSpace(r.URL.Query().Get("player_id"))
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if roomID == "" || playerID == "" || token == "" {
		http.Error(w, "missing room_id/player_id/token", http.StatusBadRequest)
		return
	}
	if !s.verifyToken(token, playerID) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// 带 ticket 的 room websocket 连接直接进入游戏消息流，实现单连接承载房间/游戏消息。
	if ticket != "" {
		s.handleWSGame(w, r)
		return
	}
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	s.roomMu.Lock()
	if old := s.roomWS[playerID]; old != nil {
		_ = old.Close()
	}
	s.roomWS[playerID] = conn
	s.roomOfPlayer[playerID] = roomID
	s.roomLastHB[playerID] = time.Now().UnixMilli()
	s.ensureRoomMonitorLocked(roomID)
	s.roomMu.Unlock()

	defer func() {
		s.roomMu.Lock()
		if cur := s.roomWS[playerID]; cur == conn {
			delete(s.roomWS, playerID)
		}
		s.roomMu.Unlock()
		_ = conn.Close()
	}()

	for {
		var req map[string]any
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		s.roomMu.Lock()
		s.roomLastHB[playerID] = time.Now().UnixMilli()
		s.roomMu.Unlock()
		if typ, _ := req["type"].(string); typ == "heartbeat" {
			if !s.refreshToken(token) {
				return
			}
		}
	}
}

func (s *Server) verifyToken(token, playerID string) bool {
	if s.authVerifier == nil || token == "" || playerID == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.TokenVerifyTimeout)
	defer cancel()
	uid, ok, _, err := s.authVerifier.VerifyToken(ctx, token)
	if err != nil || !ok {
		return false
	}
	return uid == playerID
}

func (s *Server) refreshToken(token string) bool {
	if s.authVerifier == nil || token == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.TokenVerifyTimeout)
	defer cancel()
	ok, _, err := s.authVerifier.RefreshToken(ctx, token)
	if err != nil || !ok {
		return false
	}
	return true
}

func (s *Server) ensureRoomMonitorLocked(roomID string) {
	if roomID == "" {
		return
	}
	if s.roomMonitor[roomID] {
		return
	}
	s.roomMonitor[roomID] = true
	go s.runSingleRoomHeartbeatMonitor(roomID)
}

func (s *Server) runSingleRoomHeartbeatMonitor(roomID string) {
	tk := time.NewTicker(1 * time.Second)
	defer tk.Stop()
	defer func() {
		s.roomMu.Lock()
		delete(s.roomMonitor, roomID)
		s.roomMu.Unlock()
	}()

	timeoutMs := s.cfg.HeartbeatTimeout.Milliseconds()
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}

	for range tk.C {
		now := time.Now().UnixMilli()
		playerCount := 0
		toLeave := make([]string, 0)

		s.roomMu.Lock()
		for pid, rid := range s.roomOfPlayer {
			if rid != roomID {
				continue
			}
			playerCount++
			if hb := s.roomLastHB[pid]; hb > 0 && now-hb > timeoutMs {
				toLeave = append(toLeave, pid)
			}
		}
		s.roomMu.Unlock()

		for _, pid := range toLeave {
			_, _ = s.roomMgr.LeaveRoom(pid)
			s.roomMu.Lock()
			if c := s.roomWS[pid]; c != nil {
				_ = c.Close()
			}
			delete(s.roomWS, pid)
			delete(s.roomLastHB, pid)
			delete(s.roomOfPlayer, pid)
			s.roomMu.Unlock()
		}

		if playerCount == 0 {
			return
		}
	}
}
