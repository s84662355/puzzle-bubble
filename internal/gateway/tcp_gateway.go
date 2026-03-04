package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"game-gateway/internal/config"
	"game-gateway/internal/protocol"
	"game-gateway/internal/serviceproxy"

	"github.com/gorilla/websocket"
)

type Session struct {
	ID            uint64
	Conn          net.Conn
	ConnectedAt   time.Time
	PlayerID      string
	Token         string
	Authenticated bool
	LastSeen      time.Time
}

type TokenVerifier interface {
	VerifyToken(ctx context.Context, token string) (playerID string, ok bool, reason string, err error)
	RefreshToken(ctx context.Context, token string) (ok bool, reason string, err error)
}

type TCPGateway struct {
	cfg         config.Config
	logger      *slog.Logger
	verifier    TokenVerifier
	dispatcher  serviceproxy.Dispatcher
	listener    net.Listener
	wsListener  net.Listener
	wsServer    *http.Server
	closed      atomic.Bool
	sessions    sync.Map
	playerIndex sync.Map // map[playerID]*Session
	seq         atomic.Uint64
	wsMu        sync.Mutex
	wsByPlayer  map[string]*websocket.Conn // playerID -> ws
	wsToken     map[string]string          // playerID -> token
}

func NewTCPGateway(cfg config.Config, logger *slog.Logger, verifier TokenVerifier, dispatcher serviceproxy.Dispatcher) (*TCPGateway, error) {
	if verifier == nil {
		return nil, errors.New("nil token verifier")
	}
	if dispatcher == nil {
		return nil, errors.New("nil dispatcher")
	}
	return &TCPGateway{
		cfg:        cfg,
		logger:     logger,
		verifier:   verifier,
		dispatcher: dispatcher,
		wsByPlayer: make(map[string]*websocket.Conn),
		wsToken:    make(map[string]string),
	}, nil
}

func (g *TCPGateway) Start() error {
	ln, err := net.Listen("tcp", g.cfg.ListenTCPAddr)
	if err != nil {
		return err
	}
	g.listener = ln

	for {
		conn, err := ln.Accept()
		if err != nil {
			if g.closed.Load() || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go g.handleConn(conn)
	}
}

func (g *TCPGateway) Stop(ctx context.Context) error {
	g.closed.Store(true)
	if g.listener != nil {
		_ = g.listener.Close()
	}
	if g.wsListener != nil {
		_ = g.wsListener.Close()
	}
	if g.wsServer != nil {
		_ = g.wsServer.Shutdown(ctx)
	}

	done := make(chan struct{})
	go func() {
		g.sessions.Range(func(_, value any) bool {
			s := value.(*Session)
			_ = s.Conn.Close()
			return true
		})
		g.wsMu.Lock()
		for pid, c := range g.wsByPlayer {
			_ = c.Close()
			delete(g.wsByPlayer, pid)
		}
		g.wsToken = make(map[string]string)
		g.wsMu.Unlock()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		_ = g.dispatcher.Close()
		return nil
	}
}

func (g *TCPGateway) StartWS() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/lobby", g.handleLobbyWS)
	srv := &http.Server{
		Addr:              g.cfg.WSListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ln, err := net.Listen("tcp", g.cfg.WSListenAddr)
	if err != nil {
		return err
	}
	g.wsListener = ln
	g.wsServer = srv
	return srv.Serve(ln)
}

func (g *TCPGateway) handleConn(conn net.Conn) {
	defer conn.Close()
	sessionID := g.seq.Add(1)
	s := &Session{
		ID:          sessionID,
		Conn:        conn,
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
	}
	g.sessions.Store(sessionID, s)
	defer func() {
		g.sessions.Delete(sessionID)
		g.unbindPlayerSession(s)
	}()

	g.logger.Info("session connected", "session_id", s.ID, "remote", conn.RemoteAddr().String())

	for {
		_ = conn.SetReadDeadline(time.Now().Add(g.cfg.ReadTimeout))
		pkt, err := protocol.ReadPacket(conn, g.cfg.MaxPacketSize)
		if err != nil {
			if errors.Is(err, io.EOF) {
				g.logger.Info("session disconnected", "session_id", s.ID)
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if !s.Authenticated && time.Since(s.ConnectedAt) > g.cfg.AuthTimeout {
					g.logger.Info("session auth timeout", "session_id", s.ID, "auth_timeout", g.cfg.AuthTimeout.String())
					return
				}
				if time.Since(s.LastSeen) > g.cfg.IdleTimeout {
					g.logger.Info("session idle timeout", "session_id", s.ID, "idle_timeout", g.cfg.IdleTimeout.String())
					return
				}
				continue
			}
			g.logger.Warn("read packet failed", "session_id", s.ID, "error", err)
			return
		}

		s.LastSeen = time.Now()
		if err := g.dispatch(s, pkt); err != nil {
			g.logger.Warn("dispatch failed", "session_id", s.ID, "cmd", pkt.Cmd, "error", err)
			return
		}
	}
}

func (g *TCPGateway) dispatch(s *Session, pkt protocol.Packet) error {
	switch pkt.Cmd {
	case protocol.CmdAuthReq:
		return g.handleAuth(s, pkt)
	case protocol.CmdHeartbeatReq:
		return g.handleHeartbeat(s, pkt.Seq)
	case protocol.CmdGameMessage:
		return g.handleGameMessage(s, pkt)
	default:
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdError,
			Seq:     pkt.Seq,
			Payload: []byte(`{"code":4001,"msg":"unknown cmd"}`),
		})
	}
}

func (g *TCPGateway) handleAuth(s *Session, pkt protocol.Packet) error {
	type authReq struct {
		Token string `json:"token"`
	}
	req := authReq{}
	if err := json.Unmarshal(pkt.Payload, &req); err != nil {
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdAuthResp,
			Seq:     pkt.Seq,
			Payload: []byte(`{"ok":false,"reason":"bad_json"}`),
		})
	}
	if req.Token == "" {
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdAuthResp,
			Seq:     pkt.Seq,
			Payload: []byte(`{"ok":false,"reason":"missing_token"}`),
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.cfg.TokenVerifyTimeout)
	defer cancel()
	playerID, ok, reason, err := g.verifier.VerifyToken(ctx, req.Token)
	if err != nil {
		g.logger.Error("verify token failed", "session_id", s.ID, "error", err)
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdAuthResp,
			Seq:     pkt.Seq,
			Payload: []byte(`{"ok":false,"reason":"verify_service_error"}`),
		})
	}
	if !ok || playerID == "" {
		if reason == "" {
			reason = "invalid_token"
		}
		payload, _ := json.Marshal(map[string]any{
			"ok":     false,
			"reason": reason,
		})
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdAuthResp,
			Seq:     pkt.Seq,
			Payload: payload,
		})
	}

	s.Authenticated = true
	s.PlayerID = playerID
	s.Token = req.Token
	g.bindUniquePlayerSession(s)
	payload, _ := json.Marshal(map[string]any{
		"ok":        true,
		"player_id": s.PlayerID,
	})
	return g.writePacket(s, protocol.Packet{
		Cmd:     protocol.CmdAuthResp,
		Seq:     pkt.Seq,
		Payload: payload,
	})
}

func (g *TCPGateway) handleHeartbeat(s *Session, seq uint32) error {
	if !s.Authenticated {
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdError,
			Seq:     seq,
			Payload: []byte(`{"code":4002,"msg":"unauthorized"}`),
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.cfg.TokenVerifyTimeout)
	defer cancel()
	ok, reason, err := g.verifier.RefreshToken(ctx, s.Token)
	if err != nil {
		g.logger.Error("refresh token failed", "session_id", s.ID, "error", err)
		_ = g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdError,
			Seq:     seq,
			Payload: []byte(`{"code":5001,"msg":"refresh_token_failed"}`),
		})
		return errors.New("refresh token failed")
	}
	if !ok {
		if reason == "" {
			reason = "token_invalid"
		}
		payload, _ := json.Marshal(map[string]any{
			"code":   4006,
			"msg":    "token_invalid",
			"reason": reason,
		})
		_ = g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdError,
			Seq:     seq,
			Payload: payload,
		})
		return errors.New("token invalid on heartbeat")
	}
	return g.writePacket(s, protocol.Packet{
		Cmd:     protocol.CmdHeartbeatResp,
		Seq:     seq,
		Payload: []byte(`{"ts":` + time.Now().UTC().Format("20060102150405") + `}`),
	})
}

func (g *TCPGateway) handleGameMessage(s *Session, pkt protocol.Packet) error {
	if !s.Authenticated {
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdError,
			Seq:     pkt.Seq,
			Payload: []byte(`{"code":4002,"msg":"unauthorized"}`),
		})
	}

	var req protocol.RouteEnvelope
	if err := json.Unmarshal(pkt.Payload, &req); err != nil {
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdGameMessage,
			Seq:     pkt.Seq,
			Payload: []byte(`{"code":4003,"msg":"bad_route_message_json"}`),
		})
	}
	if req.ServiceID == 0 || req.MsgID == 0 {
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdGameMessage,
			Seq:     pkt.Seq,
			Payload: []byte(`{"code":4004,"msg":"missing_service_or_msg_id"}`),
		})
	}
	if req.ServiceID == protocol.ServiceAuth {
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdGameMessage,
			Seq:     pkt.Seq,
			Payload: []byte(`{"code":4007,"msg":"auth_service_not_allowed_after_login"}`),
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.cfg.TokenVerifyTimeout)
	defer cancel()
	resp, err := g.dispatcher.Dispatch(ctx, s.PlayerID, req)
	if err != nil {
		g.logger.Error("service dispatch error", "session_id", s.ID, "service_id", req.ServiceID, "msg_id", req.MsgID, "error", err)
		return g.writePacket(s, protocol.Packet{
			Cmd:     protocol.CmdGameMessage,
			Seq:     pkt.Seq,
			Payload: []byte(`{"code":5000,"msg":"internal_error"}`),
		})
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return g.writePacket(s, protocol.Packet{
		Cmd:     protocol.CmdGameMessage,
		Seq:     pkt.Seq,
		Payload: data,
	})
}

func (g *TCPGateway) writePacket(s *Session, pkt protocol.Packet) error {
	_ = s.Conn.SetWriteDeadline(time.Now().Add(g.cfg.WriteTimeout))
	return protocol.WritePacket(s.Conn, pkt)
}

func (g *TCPGateway) PushToPlayer(playerID string, cmd uint16, payload []byte) (int, error) {
	if playerID == "" {
		return 0, errors.New("empty player_id")
	}
	pushed := 0
	var firstErr error

	if v, ok := g.playerIndex.Load(playerID); ok {
		s := v.(*Session)
		if err := g.writePacket(s, protocol.Packet{
			Cmd:     cmd,
			Seq:     uint32(time.Now().UnixNano()),
			Payload: payload,
		}); err != nil {
			firstErr = err
		} else {
			pushed++
		}
	}

	g.wsMu.Lock()
	ws := g.wsByPlayer[playerID]
	g.wsMu.Unlock()
	if ws != nil {
		if err := g.writeWSPayload(ws, cmd, payload); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			_ = ws.Close()
			g.wsMu.Lock()
			delete(g.wsByPlayer, playerID)
			delete(g.wsToken, playerID)
			g.wsMu.Unlock()
		} else {
			pushed++
		}
	}

	return pushed, firstErr
}

func (g *TCPGateway) PushToSession(sessionID uint64, cmd uint16, payload []byte) error {
	v, ok := g.sessions.Load(sessionID)
	if !ok {
		return errors.New("session not found")
	}
	s := v.(*Session)
	return g.writePacket(s, protocol.Packet{
		Cmd:     cmd,
		Seq:     uint32(time.Now().UnixNano()),
		Payload: payload,
	})
}

func (g *TCPGateway) PushToAll(cmd uint16, payload []byte) (int, error) {
	pushed := 0
	var firstErr error
	g.playerIndex.Range(func(_, value any) bool {
		s := value.(*Session)
		if err := g.writePacket(s, protocol.Packet{
			Cmd:     cmd,
			Seq:     uint32(time.Now().UnixNano()),
			Payload: payload,
		}); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return true
		}
		pushed++
		return true
	})
	g.wsMu.Lock()
	wsTargets := make([]struct {
		playerID string
		conn     *websocket.Conn
	}, 0, len(g.wsByPlayer))
	for pid, c := range g.wsByPlayer {
		wsTargets = append(wsTargets, struct {
			playerID string
			conn     *websocket.Conn
		}{playerID: pid, conn: c})
	}
	g.wsMu.Unlock()
	for _, t := range wsTargets {
		if err := g.writeWSPayload(t.conn, cmd, payload); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			_ = t.conn.Close()
			g.wsMu.Lock()
			delete(g.wsByPlayer, t.playerID)
			delete(g.wsToken, t.playerID)
			g.wsMu.Unlock()
			continue
		}
		pushed++
	}
	return pushed, firstErr
}

func (g *TCPGateway) OnlineCount() int {
	n := 0
	g.sessions.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

func (g *TCPGateway) bindUniquePlayerSession(s *Session) {
	if s.PlayerID == "" {
		return
	}
	if v, ok := g.playerIndex.Load(s.PlayerID); ok {
		old := v.(*Session)
		if old.ID != s.ID {
			_ = g.writePacket(old, protocol.Packet{
				Cmd:     protocol.CmdError,
				Seq:     uint32(time.Now().UnixNano()),
				Payload: []byte(`{"code":4005,"msg":"kicked_by_new_login"}`),
			})
			_ = old.Conn.Close()
		}
	}
	g.playerIndex.Store(s.PlayerID, s)
}

func (g *TCPGateway) unbindPlayerSession(s *Session) {
	if s.PlayerID == "" {
		return
	}
	v, ok := g.playerIndex.Load(s.PlayerID)
	if ok {
		current := v.(*Session)
		if current.ID == s.ID {
			g.playerIndex.Delete(s.PlayerID)
		}
	}
}

func (g *TCPGateway) SnapshotNodeInfo() map[string]any {
	return map[string]any{
		"online_count": g.OnlineCount(),
		"listen_tcp":   g.cfg.ListenTCPAddr,
		"listen_ws":    g.cfg.WSListenAddr,
		"now_unix":     strconv.FormatInt(time.Now().Unix(), 10),
	}
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

func (g *TCPGateway) handleLobbyWS(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), g.cfg.TokenVerifyTimeout)
	playerID, ok, _, err := g.verifier.VerifyToken(ctx, token)
	cancel()
	if err != nil || !ok || playerID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	g.wsMu.Lock()
	if old := g.wsByPlayer[playerID]; old != nil {
		_ = old.Close()
	}
	g.wsByPlayer[playerID] = conn
	g.wsToken[playerID] = token
	g.wsMu.Unlock()

	_ = conn.WriteJSON(map[string]any{"type": "lobby_connected", "player_id": playerID})

	defer func() {
		g.wsMu.Lock()
		if cur := g.wsByPlayer[playerID]; cur == conn {
			delete(g.wsByPlayer, playerID)
			delete(g.wsToken, playerID)
		}
		g.wsMu.Unlock()
		_ = conn.Close()
	}()

	for {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		if typ, _ := msg["type"].(string); typ == "heartbeat" {
			ctx, cancel := context.WithTimeout(context.Background(), g.cfg.TokenVerifyTimeout)
			ok, _, err := g.verifier.RefreshToken(ctx, token)
			cancel()
			if err != nil || !ok {
				_ = conn.WriteJSON(map[string]any{"type": "error", "msg": "token_invalid"})
				return
			}
			_ = conn.WriteJSON(map[string]any{"type": "heartbeat_ack", "ts": time.Now().UnixMilli()})
		}
	}
}

func (g *TCPGateway) writeWSPayload(conn *websocket.Conn, cmd uint16, payload []byte) error {
	var out any
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &out); err != nil {
			out = map[string]any{
				"cmd":     cmd,
				"payload": string(payload),
			}
		}
	} else {
		out = map[string]any{"cmd": cmd}
	}
	return conn.WriteJSON(out)
}
