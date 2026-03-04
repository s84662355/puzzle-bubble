package game

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type SessionView struct {
	ID       uint64
	PlayerID string
}

type Message struct {
	Module string         `json:"module"`
	Action string         `json:"action"`
	Body   map[string]any `json:"body"`
}

type Response struct {
	Code int            `json:"code"`
	Msg  string         `json:"msg"`
	Body map[string]any `json:"body,omitempty"`
}

type Handler func(ctx context.Context, s SessionView, msg Message) (Response, error)

type Router struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewRouter() *Router {
	return &Router{
		handlers: make(map[string]Handler),
	}
}

func (r *Router) Register(module, action string, h Handler) error {
	if module == "" || action == "" || h == nil {
		return errors.New("invalid handler registration")
	}
	key := routeKey(module, action)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[key]; exists {
		return fmt.Errorf("handler already exists: %s", key)
	}
	r.handlers[key] = h
	return nil
}

func (r *Router) Dispatch(ctx context.Context, s SessionView, msg Message) (Response, error) {
	key := routeKey(msg.Module, msg.Action)
	r.mu.RLock()
	h, ok := r.handlers[key]
	r.mu.RUnlock()
	if !ok {
		return Response{
			Code: 404,
			Msg:  "route_not_found",
		}, nil
	}
	return h(ctx, s, msg)
}

func routeKey(module, action string) string {
	return module + "/" + action
}
