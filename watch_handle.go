package e2b

import (
	"context"
	"errors"
	"io"
	"sync"

	"connectrpc.com/connect"

	fspb "github.com/eric642/e2b-go-sdk/internal/envd/filesystem"
)

// WatchHandle streams filesystem events from Filesystem.Watch.
type WatchHandle struct {
	events chan FilesystemEvent
	done   chan struct{}
	cancel context.CancelFunc

	mu  sync.Mutex
	err error
}

// Events returns a receive-only channel that yields filesystem events. The
// channel is closed when the watch ends; check Err() afterwards.
func (w *WatchHandle) Events() <-chan FilesystemEvent { return w.events }

// Err returns the error (if any) that terminated the watch.
func (w *WatchHandle) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Stop cancels the watch and waits for the background goroutine to finish.
func (w *WatchHandle) Stop(ctx context.Context) error {
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return contextErr(ctx.Err())
	}
}

// consume drains the Connect server stream. It closes events and done when
// the stream terminates.
func (w *WatchHandle) consume(stream *connect.ServerStreamForClient[fspb.WatchDirResponse]) {
	defer close(w.events)
	defer close(w.done)
	for stream.Receive() {
		msg := stream.Msg()
		if msg == nil {
			continue
		}
		switch ev := msg.Event.(type) {
		case *fspb.WatchDirResponse_Filesystem:
			w.events <- FilesystemEvent{
				Name: ev.Filesystem.GetName(),
				Type: fsEventFromPB(ev.Filesystem.GetType()),
			}
		case *fspb.WatchDirResponse_Start, *fspb.WatchDirResponse_Keepalive:
			// ignore
		}
	}
	if err := stream.Err(); err != nil && !errors.Is(err, io.EOF) {
		w.mu.Lock()
		w.err = mapConnectErr(err)
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
