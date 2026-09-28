package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func queueTestSession(t *testing.T) (*Session, *bytes.Buffer) {
	t.Helper()
	sess := NewSession()
	var out bytes.Buffer
	sess.Display.Out = &out
	sess.Display.Term = &fakeTerm{}
	sess.Display.ActiveTab = TabChat
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Chain.TipPath = t.TempDir() + "/tip.json"
	return sess, &out
}

// TestOutputQueuePreservesOrder: chat and local lines keep their exact
// emission order through the async queue.
func TestOutputQueuePreservesOrder(t *testing.T) {
	sess, out := queueTestSession(t)

	sess.Display.DisplayMu.Lock()
	for i := 0; i < 50; i++ {
		sess.emitTab(TabChat, fmt.Sprintf("chat %02d\n", i))
		sess.emitLocalFeedback(fmt.Sprintf("sys %02d\n", i))
	}
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	var want strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&want, "chat %02d\nsys %02d\n", i, i)
	}
	if out.String() != want.String() {
		t.Fatalf("queue reordered output: got %q", out.String())
	}
}

// gateWriter parks its first Write until released, and signals when it
// is parked so a test can enqueue with the drainer provably idle. This
// makes cap-overflow tests deterministic instead of racing the flusher.
type gateWriter struct {
	hold    chan struct{}
	entered chan struct{}
	dst     io.Writer
	started atomic.Bool
}

func (w *gateWriter) Write(p []byte) (int, error) {
	if w.started.CompareAndSwap(false, true) {
		close(w.entered)
		<-w.hold
	}
	return w.dst.Write(p)
}

// TestOutputQueueDropsOldestAtCap: a stalled terminal bounds memory by
// dropping the oldest rendered chunks at line boundaries; data stays in
// the tab buffers.
func TestOutputQueueDropsOldestAtCap(t *testing.T) {
	sess, out := queueTestSession(t)
	hold := make(chan struct{})
	gw := &gateWriter{hold: hold, entered: make(chan struct{}), dst: out}
	sess.Display.Out = gw

	// Park the drainer on its first write, then enqueue with it idle.
	sess.emitTab(TabChat, "warmup\n")
	<-gw.entered

	total := 0
	sess.Display.DisplayMu.Lock()
	for i := 0; i < 600; i++ {
		line := fmt.Sprintf("line-%04d %s\n", i, strings.Repeat("y", 1000))
		total += len(line)
		sess.emitTab(TabChat, line)
	}
	sess.Display.DisplayMu.Unlock()

	if sess.Display.OutSkipped == 0 {
		t.Fatal("expected queue drops past the cap")
	}
	close(hold)
	sess.flushOutputNow()
	if got := out.Len(); got >= total {
		t.Fatalf("no bytes lost: flushed %d of %d", got, total)
	}
	if !strings.Contains(out.String(), "line-0599") {
		t.Fatal("newest chunk must survive")
	}
}

// TestOutputQueueCapsNewlineFreeChunks: a burst of escape-only chunks
// (no newline) still bounds memory instead of leaking past the cap.
func TestOutputQueueCapsNewlineFreeChunks(t *testing.T) {
	sess, out := queueTestSession(t)
	hold := make(chan struct{})
	gw := &gateWriter{hold: hold, entered: make(chan struct{}), dst: out}
	sess.Display.Out = gw

	sess.emitTab(TabChat, "warmup\n")
	<-gw.entered

	chunk := "\033[1A\033[2K\r" + strings.Repeat("x", 4096)
	sess.Display.DisplayMu.Lock()
	for i := 0; i < 400; i++ {
		sess.enqueueOutput(chunk)
	}
	sess.Display.DisplayMu.Unlock()

	sess.Display.OutMu.Lock()
	queued := len(sess.Display.OutBuf)
	sess.Display.OutMu.Unlock()
	if queued > outBufCap {
		t.Fatalf("queue = %d bytes, cap %d", queued, outBufCap)
	}
	if sess.Display.OutSkipped == 0 {
		t.Fatal("expected newline-free overflow to drop")
	}
	close(hold)
	sess.flushOutputNow()
}

// TestOutputQueueConcurrent: racing producers serialize into one ordered
// byte stream; under -race this also pins the lock discipline.
func TestOutputQueueConcurrent(t *testing.T) {
	sess, out := queueTestSession(t)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				sess.enqueueOutput(fmt.Sprintf("g%d-%d\n", g, i))
			}
		}(g)
	}
	wg.Wait()
	sess.flushOutputNow()

	total := 0
	for g := 0; g < 8; g++ {
		for i := 0; i < 200; i++ {
			line := fmt.Sprintf("g%d-%d\n", g, i)
			if n := strings.Count(out.String(), line); n != 1 {
				t.Fatalf("line %q appears %d times, want 1", line, n)
			}
			total += len(line)
		}
	}
	if out.Len() != total {
		t.Fatalf("flushed %d bytes, want %d", out.Len(), total)
	}
}
