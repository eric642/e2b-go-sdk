package e2b

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

func TestIntegrationFilesystemWriteReadRoundTrip(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	if _, err := sbx.Files.WriteString(ctx, "/tmp/hello.txt", "round-trip", FsOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := sbx.Files.Read(ctx, "/tmp/hello.txt", FsOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != "round-trip" {
		t.Fatalf("content: %q", data)
	}
}

func TestIntegrationFilesystemWriteBytes(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	payload := []byte{0x00, 0x01, 0x02, 0xff, 0xfe}
	if _, err := sbx.Files.Write(ctx, "/tmp/bin.dat", bytes.NewReader(payload), FsOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := sbx.Files.Read(ctx, "/tmp/bin.dat", FsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("binary round-trip diverged: %x vs %x", got, payload)
	}
}

func TestIntegrationFilesystemReadStream(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	if _, err := sbx.Files.WriteString(ctx, "/tmp/stream.txt", "streaming", FsOptions{}); err != nil {
		t.Fatal(err)
	}
	rc, err := sbx.Files.ReadStream(ctx, "/tmp/stream.txt", FsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	buf, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf) != "streaming" {
		t.Fatalf("stream body: %q", buf)
	}
}

func TestIntegrationFilesystemExistsStat(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	// Sanity: /tmp always exists on Linux.
	ok, err := sbx.Files.Exists(ctx, "/tmp", FsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("/tmp should exist")
	}
	info, err := sbx.Files.Stat(ctx, "/tmp", FsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != EntryTypeDirectory {
		t.Fatalf("/tmp should be a directory, got %v", info.Type)
	}

	// Missing file → Exists false, Stat error.
	ok, err = sbx.Files.Exists(ctx, "/tmp/nope-deadbeef", FsOptions{})
	if err != nil {
		t.Fatalf("Exists on missing file should swallow error, got %v", err)
	}
	if ok {
		t.Fatal("Exists reported true for missing path")
	}
	_, err = sbx.Files.Stat(ctx, "/tmp/nope-deadbeef", FsOptions{})
	var fnf *FileNotFoundError
	if !errors.As(err, &fnf) {
		t.Fatalf("Stat missing file should return *FileNotFoundError, got %T: %v", err, err)
	}
}

func TestIntegrationFilesystemMakeDirList(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	if err := sbx.Files.MakeDir(ctx, "/tmp/mkdir-test/nested", FsOptions{}); err != nil {
		t.Fatal(err)
	}
	// Put a file inside so List has something to return.
	if _, err := sbx.Files.WriteString(ctx, "/tmp/mkdir-test/nested/f.txt", "x", FsOptions{}); err != nil {
		t.Fatal(err)
	}
	entries, err := sbx.Files.List(ctx, "/tmp/mkdir-test/nested", FsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var foundFile bool
	for _, e := range entries {
		if e.Name == "f.txt" && e.Type == EntryTypeFile {
			foundFile = true
		}
	}
	if !foundFile {
		t.Fatalf("List did not return f.txt: %+v", entries)
	}
}

func TestIntegrationFilesystemMoveRemove(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	if _, err := sbx.Files.WriteString(ctx, "/tmp/src.txt", "move me", FsOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := sbx.Files.Move(ctx, "/tmp/src.txt", "/tmp/dst.txt", FsOptions{}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := sbx.Files.Exists(ctx, "/tmp/src.txt", FsOptions{}); ok {
		t.Fatal("source should be gone after Move")
	}
	if err := sbx.Files.Remove(ctx, "/tmp/dst.txt", FsOptions{}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := sbx.Files.Exists(ctx, "/tmp/dst.txt", FsOptions{}); ok {
		t.Fatal("file should be removed")
	}
}

func TestIntegrationFilesystemWatch(t *testing.T) {
	sbx := newIntegrationSandbox(t, CreateOptions{})
	ctx, cancel := integrationContext(t)
	defer cancel()

	dir := "/tmp/watch-test"
	if err := sbx.Files.MakeDir(ctx, dir, FsOptions{}); err != nil {
		t.Fatal(err)
	}
	watch, err := sbx.Files.Watch(ctx, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	// Give envd a moment to register the watch before we write.
	time.Sleep(500 * time.Millisecond)

	go func() {
		_, _ = sbx.Files.WriteString(ctx, dir+"/new.txt", "hi", FsOptions{})
	}()

	select {
	case ev, ok := <-watch.Events():
		if !ok {
			t.Fatal("watch channel closed before event")
		}
		if ev.Name == "" {
			t.Fatalf("empty event: %+v", ev)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}

	stopCtx, stopCancel := integrationContext(t)
	defer stopCancel()
	_ = watch.Stop(stopCtx)
}
