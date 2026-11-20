package e2b

import (
	"context"
	"errors"
	"io"
	"sync"

	"connectrpc.com/connect"

	fspb "github.com/eric642/e2b-go-sdk/internal/envd/filesystem"
)

// WatchHandle streams filesystem events from Filesystem.Watch and
// Filesystem.WatchWithOptions.
type WatchHandle struct {
	ctx        context.Context
	events     chan FilesystemEvent
	done       chan struct{}
	cancel     context.CancelFunc
	cancelOnce sync.Once

	mu      sync.Mutex
	stopped bool
	err     error
}

func (w *WatchHandle) invokeCancel() {
	w.cancelOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
}

// Events returns a receive-only channel that yields filesystem events. The
// channel is closed when the watch ends; check Err() afterwards.
func (w *WatchHandle) Events() <-chan FilesystemEvent { return w.events }

// Err returns the error (if any) that terminated the watch. Returns nil after
// a clean Stop.
func (w *WatchHandle) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Stop cancels the watch stream locally and waits for the background goroutine
// to finish. Calling Stop multiple times is safe.
func (w *WatchHandle) Stop(ctx context.Context) error {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	w.invokeCancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return contextErr(ctx.Err())
	}
}

func (w *WatchHandle) consume(stream *connect.ServerStreamForClient[fspb.WatchDirResponse]) {
	defer w.invokeCancel()
	defer func() { _ = stream.Close() }()
	defer close(w.events)
	defer close(w.done)
	for stream.Receive() {
		msg := stream.Msg()
		if msg == nil {
			continue
		}
		switch ev := msg.Event.(type) {
		case *fspb.WatchDirResponse_Filesystem:
			if ev.Filesystem == nil {
				continue
			}
			out := FilesystemEvent{
				Name: ev.Filesystem.GetName(),
				Type: fsEventFromPB(ev.Filesystem.GetType()),
			}
			if ev.Filesystem.Entry != nil {
				out.Entry = entryInfoFromPB(ev.Filesystem.Entry)
			}
			if w.ctx != nil {
				select {
				case w.events <- out:
				case <-w.ctx.Done():
					return
				}
			} else {
				w.events <- out
			}
		case *fspb.WatchDirResponse_Start, *fspb.WatchDirResponse_Keepalive:
		}
	}
	if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
		w.mu.Lock()
		if !w.stopped {
			w.err = mapConnectErr(err)
		}
		w.mu.Unlock()
	}
}

func fsEventFromPB(t fspb.EventType) FilesystemEventType {
	switch t {
	case fspb.EventType_EVENT_TYPE_CREATE:
		return FsEventCreate
	case fspb.EventType_EVENT_TYPE_WRITE:
		return FsEventWrite
	case fspb.EventType_EVENT_TYPE_REMOVE:
		return FsEventRemove
	case fspb.EventType_EVENT_TYPE_RENAME:
		return FsEventRename
	case fspb.EventType_EVENT_TYPE_CHMOD:
		return FsEventChmod
	}
	return FsEventUnspecified
}
