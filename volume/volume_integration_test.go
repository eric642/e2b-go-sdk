//go:build integration

package volume_test

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/eric642/e2b-go-sdk"
	"github.com/eric642/e2b-go-sdk/volume"
)

func skipIfNoAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv("E2B_API_KEY") == "" {
		t.Skip("E2B_API_KEY not set; skipping integration test")
	}
}

func integrationCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

func randomName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), rand.Intn(1_000_000))
}

func TestIntegrationVolumeLifecycle(t *testing.T) {
	skipIfNoAPIKey(t)
	ctx, cancel := integrationCtx(t)
	defer cancel()

	name := randomName("go-itest")
	v, err := volume.Create(ctx, name, volume.Options{Config: e2b.Config{}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		_ = v.Delete(context.Background())
	})
	if v.ID == "" || v.Token() == "" {
		t.Fatalf("Create returned empty id/token: %+v", v)
	}

	// Write + read round-trip.
	content := []byte("hello-volume")
	if err := v.WriteFile(ctx, "/file.txt", content); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := v.ReadFile(ctx, "/file.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: %q vs %q", got, content)
	}

	if err := v.MakeDir(ctx, "/sub"); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}

	// TODO: verify via Volume.List(path) once that method is added to the
	// public API (volume.go currently has no path-listing helper).

	if err := v.Remove(ctx, "/file.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestIntegrationVolumeConnect(t *testing.T) {
	skipIfNoAPIKey(t)
	ctx, cancel := integrationCtx(t)
	defer cancel()

	name := randomName("go-itest-connect")
	v, err := volume.Create(ctx, name, volume.Options{Config: e2b.Config{}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = v.Delete(context.Background()) })

	// Re-attach via Connect using the returned token.
	other, err := volume.Connect(ctx, v.ID, v.Token(), volume.Options{Config: e2b.Config{}})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if other.ID != v.ID {
		t.Fatalf("Connect returned different volume: %s vs %s", other.ID, v.ID)
	}
}
