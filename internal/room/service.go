package room

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"game-gateway/internal/protocol"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

type Config struct {
	ListenAddr      string
	ShutdownTimeout time.Duration
	DefaultCapacity int
	FrameInterval   time.Duration
}

type RoomState string

const (
	StateIdle       RoomState = "Idle"
	StateReady      RoomState = "Ready"
	StateLoading    RoomState = "Loading"
	StatePlaying    RoomState = "Playing"
	StateSettlement RoomState = "Settlement"
	StateClosed     RoomState = "Closed"
)

type Player struct {
	PlayerID string
	Ready    bool
}

type CommandEvent struct {
	Seq      uint64
	Frame    uint64
	PlayerID string
	Cmd      string
	Payload  map[string]any
	TS       int64
}

type Room struct {
	ID           string
	Name         string
	OwnerID      string
	State        RoomState
	MaxPlayers   int
	Players      map[string]*Player
	CmdSeq       uint64
	Commands     []CommandEvent
	PlayingSince time.Time
}

type Manager struct {
	mu         sync.Mutex
	rooms      map[string]*Room
	playerRoom map[string]string
	frameTick  time.Duration
}

func NewManager(frameTick time.Duration) *Manager {
	if frameTick <= 0 {
		frameTick = 50 * time.Millisecond
	}
	return &Manager{
		rooms:      make(map[string]*Room),
		playerRoom: make(map[string]string),
		frameTick:  frameTick,
	}
}

func (m *Manager) CreateRoom(playerID string, name string, capacity int) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.playerRoom[playerID]; ok {
		return nil, errors.New("player already in room")
	}
	if capacity <= 1 {
		capacity = 2
	}
	roomID := strconv.FormatInt(time.Now().UnixNano(), 10)
	r := &Room{
		ID:         roomID,
		Name:       name,
		OwnerID:    playerID,
		State:      StateIdle,
		MaxPlayers: capacity,
		Players: map[string]*Player{
			playerID: {PlayerID: playerID, Ready: false},
		},
		Commands: make([]CommandEvent, 0, 256),
	}
	m.rooms[roomID] = r
	m.playerRoom[playerID] = roomID
	return cloneRoom(r), nil
}

func (m *Manager) ListRooms() []*Room {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		out = append(out, cloneRoom(r))
	}
	return out
}

func (m *Manager) JoinRoom(playerID, roomID string) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.playerRoom[playerID]; ok {
		return nil, errors.New("player already in room")
	}
	r, ok := m.rooms[roomID]
	if !ok {
		return nil, errors.New("room not found")
	}
	if r.State == StateClosed || r.State == StateSettlement || r.State == StatePlaying {
		return nil, fmt.Errorf("room state does not allow join: %s", r.State)
	}
	if len(r.Players) >= r.MaxPlayers {
		return nil, errors.New("room full")
	}
	r.Players[playerID] = &Player{PlayerID: playerID}
	m.playerRoom[playerID] = roomID
	m.updateRoomStateLocked(r)
	return cloneRoom(r), nil
}

func (m *Manager) SetReady(playerID string, ready bool) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.mustGetPlayerRoomLocked(playerID)
	if err != nil {
		return nil, err
	}
	p := r.Players[playerID]
	p.Ready = ready
	m.updateRoomStateLocked(r)
	return cloneRoom(r), nil
}

func (m *Manager) StartGame(playerID string) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.mustGetPlayerRoomLocked(playerID)
	if err != nil {
		return nil, err
	}
	if r.OwnerID != playerID {
		return nil, errors.New("only owner can start")
	}
	if len(r.Players) < 2 {
		return nil, errors.New("need at least 2 players")
	}
	r.State = StatePlaying
	if r.PlayingSince.IsZero() {
		r.PlayingSince = time.Now()
	}
	return cloneRoom(r), nil
}

func (m *Manager) LeaveRoom(playerID string) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.mustGetPlayerRoomLocked(playerID)
	if err != nil {
		return nil, err
	}
	delete(r.Players, playerID)
	delete(m.playerRoom, playerID)

	if len(r.Players) == 0 {
		r.State = StateClosed
		delete(m.rooms, r.ID)
		return cloneRoom(r), nil
	}

	if r.OwnerID == playerID {
		for _, p := range r.Players {
			r.OwnerID = p.PlayerID
			break
		}
	}
	m.updateRoomStateLocked(r)
	return cloneRoom(r), nil
}

func (m *Manager) SubmitInput(playerID string, cmd string, payload map[string]any) (*CommandEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.mustGetPlayerRoomLocked(playerID)
	if err != nil {
		return nil, err
	}
	if r.State != StatePlaying {
		return nil, fmt.Errorf("room not in playing state: %s", r.State)
	}
	if cmd == "" {
		return nil, errors.New("empty cmd")
	}
	r.CmdSeq++
	ev := CommandEvent{
		Seq:      r.CmdSeq,
		Frame:    m.currentFrameLocked(r),
		PlayerID: playerID,
		Cmd:      cmd,
		Payload:  payload,
		TS:       time.Now().UnixMilli(),
	}
	r.Commands = append(r.Commands, ev)
	if len(r.Commands) > 2048 {
		r.Commands = r.Commands[len(r.Commands)-2048:]
	}
	cp := ev
	return &cp, nil
}

func (m *Manager) PullInputs(playerID string, lastSeq uint64) ([]CommandEvent, uint64, *Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.mustGetPlayerRoomLocked(playerID)
	if err != nil {
		return nil, 0, nil, err
	}
	events := make([]CommandEvent, 0, 32)
	for _, ev := range r.Commands {
		if ev.Seq > lastSeq {
			events = append(events, ev)
		}
	}
	return events, m.currentFrameLocked(r), cloneRoom(r), nil
}

func (m *Manager) Snapshot(playerID string) (*Room, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.mustGetPlayerRoomLocked(playerID)
	if err != nil {
		return nil, 0, err
	}
	return cloneRoom(r), m.currentFrameLocked(r), nil
}

func (m *Manager) mustGetPlayerRoomLocked(playerID string) (*Room, error) {
	roomID, ok := m.playerRoom[playerID]
	if !ok {
		return nil, errors.New("player not in room")
	}
	r, ok := m.rooms[roomID]
	if !ok {
		return nil, errors.New("room not found")
	}
	return r, nil
}

func (m *Manager) updateRoomStateLocked(r *Room) {
	if len(r.Players) == 0 {
		r.State = StateClosed
		return
	}
	if len(r.Players) == 1 {
		r.State = StateIdle
		return
	}

	allReady := true
	for _, p := range r.Players {
		if !p.Ready {
			allReady = false
			break
		}
	}
	if !allReady {
		r.State = StateReady
		return
	}

	if r.State == StateIdle || r.State == StateReady || r.State == StateLoading {
		r.State = StateLoading
		// Simplified loading stage to immediate playing.
		r.State = StatePlaying
		if r.PlayingSince.IsZero() {
			r.PlayingSince = time.Now()
		}
	}
}

func (m *Manager) currentFrameLocked(r *Room) uint64 {
	if r.State != StatePlaying || r.PlayingSince.IsZero() {
		return 0
	}
	return uint64(time.Since(r.PlayingSince) / m.frameTick)
}

func cloneRoom(r *Room) *Room {
	cp := &Room{
		ID:           r.ID,
		Name:         r.Name,
		OwnerID:      r.OwnerID,
		State:        r.State,
		MaxPlayers:   r.MaxPlayers,
		Players:      make(map[string]*Player, len(r.Players)),
		CmdSeq:       r.CmdSeq,
		Commands:     make([]CommandEvent, 0, len(r.Commands)),
		PlayingSince: r.PlayingSince,
	}
	for k, v := range r.Players {
		cp.Players[k] = &Player{PlayerID: v.PlayerID, Ready: v.Ready}
	}
	cp.Commands = append(cp.Commands, r.Commands...)
	return cp
}

type Server struct {
	cfg      Config
	logger   *slog.Logger
	manager  *Manager
	grpcSrv  *grpc.Server
	listener net.Listener
}

func NewServer(cfg Config, logger *slog.Logger) *Server {
	s := &Server{
		cfg:     cfg,
		logger:  logger,
		manager: NewManager(cfg.FrameInterval),
		grpcSrv: grpc.NewServer(),
	}
	registerRoomService(s.grpcSrv, &roomService{server: s})
	return s
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return err
	}
	s.listener = ln
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

type roomService struct {
	server *Server
}

func (r *roomService) HandleGatewayMessage(_ context.Context, req *structpb.Struct) (*structpb.Struct, error) {
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
		room, err := r.server.manager.CreateRoom(playerID, name, capacity)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		return mapResp(0, "ok", map[string]any{"room": roomToMap(room)}), nil
	case protocol.RoomMsgJoinRoom:
		roomID, _ := body["room_id"].(string)
		room, err := r.server.manager.JoinRoom(playerID, roomID)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		return mapResp(0, "ok", map[string]any{"room": roomToMap(room)}), nil
	case protocol.RoomMsgReady:
		ready := true
		if v, ok := body["ready"].(bool); ok {
			ready = v
		}
		room, err := r.server.manager.SetReady(playerID, ready)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		return mapResp(0, "ok", map[string]any{"room": roomToMap(room)}), nil
	case protocol.RoomMsgStartGame:
		room, err := r.server.manager.StartGame(playerID)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		return mapResp(0, "ok", map[string]any{"room": roomToMap(room)}), nil
	case protocol.RoomMsgLeaveRoom:
		room, err := r.server.manager.LeaveRoom(playerID)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		return mapResp(0, "ok", map[string]any{"room": roomToMap(room)}), nil
	case protocol.RoomMsgSubmitInput:
		cmd, _ := body["cmd"].(string)
		payload := toMap(body["payload"])
		ev, err := r.server.manager.SubmitInput(playerID, cmd, payload)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		return mapResp(0, "ok", map[string]any{"event": eventToMap(*ev)}), nil
	case protocol.RoomMsgPullInputs:
		lastSeq := uint64(toFloat64(body["last_seq"]))
		events, frame, room, err := r.server.manager.PullInputs(playerID, lastSeq)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		outEvents := make([]any, 0, len(events))
		for _, ev := range events {
			outEvents = append(outEvents, eventToMap(ev))
		}
		return mapResp(0, "ok", map[string]any{
			"frame":  frame,
			"room":   roomToMap(room),
			"events": outEvents,
		}), nil
	case protocol.RoomMsgSnapshot:
		room, frame, err := r.server.manager.Snapshot(playerID)
		if err != nil {
			return mapResp(300, err.Error(), nil), nil
		}
		return mapResp(0, "ok", map[string]any{
			"frame": frame,
			"room":  roomToMap(room),
		}), nil
	case protocol.RoomMsgListRooms:
		rooms := r.server.manager.ListRooms()
		items := make([]any, 0, len(rooms))
		for _, room := range rooms {
			items = append(items, roomToMap(room))
		}
		return mapResp(0, "ok", map[string]any{
			"rooms": items,
		}), nil
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
			{
				MethodName: "HandleGatewayMessage",
				Handler:    handleGatewayMessageHandler,
			},
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
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/room.RoomService/HandleGatewayMessage",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(roomServiceAPI).HandleGatewayMessage(ctx, req.(*structpb.Struct))
	}
	return interceptor(ctx, in, info, handler)
}

func mapResp(code int, msg string, body map[string]any) *structpb.Struct {
	if body == nil {
		body = map[string]any{}
	}
	resp, _ := structpb.NewStruct(map[string]any{
		"code": code,
		"msg":  msg,
		"body": body,
	})
	return resp
}

func roomToMap(r *Room) map[string]any {
	if r == nil {
		return map[string]any{}
	}
	players := make([]any, 0, len(r.Players))
	for _, p := range r.Players {
		players = append(players, map[string]any{
			"player_id": p.PlayerID,
			"ready":     p.Ready,
		})
	}
	return map[string]any{
		"room_id":       r.ID,
		"name":          r.Name,
		"owner_id":      r.OwnerID,
		"state":         string(r.State),
		"max_players":   r.MaxPlayers,
		"players":       players,
		"latest_cmd":    r.CmdSeq,
		"playing_since": r.PlayingSince.UnixMilli(),
	}
}

func eventToMap(ev CommandEvent) map[string]any {
	return map[string]any{
		"seq":       ev.Seq,
		"frame":     ev.Frame,
		"player_id": ev.PlayerID,
		"cmd":       ev.Cmd,
		"payload":   ev.Payload,
		"ts":        ev.TS,
	}
}

func toMap(v any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func toFloat64(v any) float64 {
	if v == nil {
		return 0
	}
	if n, ok := v.(float64); ok {
		return n
	}
	return 0
}

func toUint16(v any) uint16 {
	return uint16(toFloat64(v))
}
