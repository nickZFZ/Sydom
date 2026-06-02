package policysync

import (
	"sync"
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/stretchr/testify/require"
)

func TestHub_DispatchDelivers(t *testing.T) {
	h := NewHub(4)
	sub := h.register(7)
	defer h.unregister(sub)

	ev := &syncv1.SyncEvent{Event: &syncv1.SyncEvent_Delta{Delta: &syncv1.Delta{Version: 1}}}
	h.Dispatch(7, ev)

	select {
	case got := <-sub.events:
		require.Equal(t, uint64(1), got.GetDelta().Version)
	default:
		t.Fatal("期望收到事件")
	}
}

func TestHub_DispatchToOtherAppIgnored(t *testing.T) {
	h := NewHub(4)
	sub := h.register(7)
	defer h.unregister(sub)

	h.Dispatch(999, &syncv1.SyncEvent{}) // 非本 app
	require.Empty(t, sub.events)
}

func TestHub_OverflowSignals(t *testing.T) {
	h := NewHub(2) // 缓冲仅 2
	sub := h.register(7)
	defer h.unregister(sub)

	ev := &syncv1.SyncEvent{Event: &syncv1.SyncEvent_Delta{Delta: &syncv1.Delta{Version: 1}}}
	for i := 0; i < 5; i++ { // 灌满并溢出
		h.Dispatch(7, ev)
	}
	require.Len(t, sub.events, 2)   // 缓冲被填满
	require.Len(t, sub.overflow, 1) // size-1 去重：灌爆 3 次只投递一个信号
	select {
	case <-sub.overflow:
		// 溢出信号已投递
	default:
		t.Fatal("期望溢出信号")
	}
}

func TestHub_UnregisterStopsDelivery(t *testing.T) {
	h := NewHub(4)
	sub := h.register(7)
	h.unregister(sub)
	h.Dispatch(7, &syncv1.SyncEvent{})
	require.Empty(t, sub.events)
}

func TestHub_FanoutToMultipleSubscribers(t *testing.T) {
	h := NewHub(4)
	sub1 := h.register(7)
	sub2 := h.register(7)
	defer h.unregister(sub1)
	defer h.unregister(sub2)

	ev := &syncv1.SyncEvent{Event: &syncv1.SyncEvent_Delta{Delta: &syncv1.Delta{Version: 1}}}
	h.Dispatch(7, ev)

	require.Len(t, sub1.events, 1)
	require.Len(t, sub2.events, 1)
}

func TestHub_ConcurrentDispatchAndRegister(t *testing.T) {
	h := NewHub(8)
	ev := &syncv1.SyncEvent{Event: &syncv1.SyncEvent_Delta{Delta: &syncv1.Delta{Version: 1}}}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub := h.register(7)
			h.Dispatch(7, ev)
			h.unregister(sub)
		}()
	}
	// 并发向同一 app 持续 Dispatch
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Dispatch(7, ev)
		}()
	}
	wg.Wait()
}
