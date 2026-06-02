// Package policysync 实现控制面 PolicySync gRPC 服务端、本地 fan-out Hub 与版本对账。
package policysync

import (
	"sync"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
)

// subscriber 是一条本地 Subscribe 流的投递端点：有界事件缓冲 + size-1 溢出信号。
type subscriber struct {
	appID    int64
	events   chan *syncv1.SyncEvent // 有界数据缓冲
	overflow chan struct{}          // size-1，去重的"已落后，请全量对账"信号
}

// Hub 管理 app_id → 本地 Subscribe 流，并向其做有界非阻塞 fan-out。
type Hub struct {
	mu      sync.RWMutex
	streams map[int64]map[*subscriber]struct{}
	bufSize int
}

// NewHub 构造 Hub，bufSize 为每流事件缓冲容量。
func NewHub(bufSize int) *Hub {
	return &Hub{streams: map[int64]map[*subscriber]struct{}{}, bufSize: bufSize}
}

// register 为某 app 注册一个新流端点。
func (h *Hub) register(appID int64) *subscriber {
	s := &subscriber{
		appID:    appID,
		events:   make(chan *syncv1.SyncEvent, h.bufSize),
		overflow: make(chan struct{}, 1),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.streams[appID] == nil {
		h.streams[appID] = map[*subscriber]struct{}{}
	}
	h.streams[appID][s] = struct{}{}
	return s
}

// unregister 注销一个流端点（流结束时调用），清掉空 app 桶。
func (h *Hub) unregister(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.streams[s.appID]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.streams, s.appID)
		}
	}
}

// Dispatch 把事件非阻塞投递给某 app 的所有本地流；缓冲满则投递一次溢出信号（去重）。
func (h *Hub) Dispatch(appID int64, ev *syncv1.SyncEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.streams[appID] {
		select {
		case s.events <- ev:
		default:
			// 缓冲满：丢弃增量，投递溢出信号（size-1，已满则丢弃——信号已 pending）
			select {
			case s.overflow <- struct{}{}:
			default:
			}
		}
	}
}
