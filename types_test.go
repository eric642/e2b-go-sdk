package e2b

import (
	"testing"
	"time"

	apiclient "github.com/eric642/e2b-go-sdk/internal/api"
)

func TestSandboxInfoFromAPIMinimal(t *testing.T) {
	d := &apiclient.SandboxDetail{
		SandboxID:   "sbx-123",
		TemplateID:  "base",
		State:       apiclient.SandboxState("running"),
		CpuCount:    2,
		MemoryMB:    512,
		DiskSizeMB:  1024,
		StartedAt:   time.Unix(1700000000, 0),
		EndAt:       time.Unix(1700001000, 0),
		EnvdVersion: "v1.0",
	}
	info := sandboxInfoFromAPI(d)
	if info.SandboxID != "sbx-123" {
		t.Fatalf("SandboxID: %s", info.SandboxID)
	}
	if info.State != SandboxStateRunning {
		t.Fatalf("State: %s", info.State)
	}
	if info.CPUCount != 2 || info.MemoryMB != 512 || info.DiskSizeMB != 1024 {
		t.Fatalf("resource fields: %+v", info)
	}
	if info.Alias != "" || info.Domain != "" || info.EnvdAccessToken != "" {
		t.Fatalf("optional pointer fields should default to zero value: %+v", info)
	}
	if info.AllowInternetAccess != nil {
		t.Fatal("AllowInternetAccess must be nil when upstream left it unset")
	}
	if info.Network != nil || info.Lifecycle != nil {
		t.Fatalf("Network/Lifecycle must be nil when upstream left them unset: %+v", info)
	}
}

func TestSandboxInfoFromAPIAllFields(t *testing.T) {
	alias := "my-alias"
	domain := "example.com"
	token := "envd-tok"
	allow := true

	md := apiclient.SandboxMetadata{"key": "value"}

	allowOut := []string{"1.0.0.0/24"}
	denyOut := []string{"8.8.8.8/32"}
	maskHost := "mask.example.com"
	allowPublic := true

	d := &apiclient.SandboxDetail{
		SandboxID:           "sbx-full",
		Alias:               &alias,
		Domain:              &domain,
		EnvdAccessToken:     &token,
		AllowInternetAccess: &allow,
		TemplateID:          "template",
		State:               apiclient.SandboxState("paused"),
		CpuCount:            4,
		MemoryMB:            2048,
		DiskSizeMB:          4096,
		EnvdVersion:         "v2",
		StartedAt:           time.Unix(1, 0),
		EndAt:               time.Unix(2, 0),
		Metadata:            &md,
		Network: &apiclient.SandboxNetworkConfig{
			AllowOut:           &allowOut,
			DenyOut:            &denyOut,
			AllowPublicTraffic: &allowPublic,
			MaskRequestHost:    &maskHost,
		},
		Lifecycle: &apiclient.SandboxLifecycle{
			AutoResume: true,
			OnTimeout:  apiclient.SandboxOnTimeout("pause"),
		},
		VolumeMounts: &[]apiclient.SandboxVolumeMount{
			{Name: "data", Path: "/mnt/data"},
		},
	}
	info := sandboxInfoFromAPI(d)
	if info.Alias != alias || info.Domain != domain || info.EnvdAccessToken != token {
		t.Fatalf("string pointers not unwrapped: %+v", info)
	}
	if info.AllowInternetAccess == nil || !*info.AllowInternetAccess {
		t.Fatalf("AllowInternetAccess: %v", info.AllowInternetAccess)
	}
	if info.State != SandboxStatePaused {
		t.Fatalf("State: %s", info.State)
	}
	if info.Metadata["key"] != "value" {
		t.Fatalf("Metadata: %+v", info.Metadata)
	}
	if info.Network == nil ||
		len(info.Network.AllowOut) != 1 || info.Network.AllowOut[0] != "1.0.0.0/24" ||
		len(info.Network.DenyOut) != 1 || info.Network.DenyOut[0] != "8.8.8.8/32" ||
		info.Network.AllowPublicTraffic == nil || !*info.Network.AllowPublicTraffic ||
		info.Network.MaskRequestHost != maskHost {
		t.Fatalf("Network unmarshal broken: %+v", info.Network)
	}
	if info.Lifecycle == nil || info.Lifecycle.OnTimeout != "pause" || !info.Lifecycle.AutoResume {
		t.Fatalf("Lifecycle unmarshal broken: %+v", info.Lifecycle)
	}
	if len(info.VolumeMounts) != 1 || info.VolumeMounts[0].Name != "data" || info.VolumeMounts[0].Path != "/mnt/data" {
		t.Fatalf("VolumeMounts: %+v", info.VolumeMounts)
	}
}

func TestSandboxInfoFromAPINetworkPartialFields(t *testing.T) {
	allowOut := []string{"10.0.0.0/8"}
	d := &apiclient.SandboxDetail{
		SandboxID: "sbx",
		Network: &apiclient.SandboxNetworkConfig{
			AllowOut: &allowOut,
		},
	}
	info := sandboxInfoFromAPI(d)
	if info.Network == nil {
		t.Fatal("Network must be non-nil since upstream sent one field")
	}
	if len(info.Network.DenyOut) != 0 {
		t.Fatalf("DenyOut should be empty slice, got %v", info.Network.DenyOut)
	}
	if info.Network.AllowPublicTraffic != nil {
		t.Fatalf("AllowPublicTraffic should default to nil when omitted, got %v", *info.Network.AllowPublicTraffic)
	}
	if info.Network.MaskRequestHost != "" {
		t.Fatalf("MaskRequestHost should default to empty, got %q", info.Network.MaskRequestHost)
	}
}

func TestEntryTypeConstants(t *testing.T) {
	if EntryTypeUnspecified != 0 || EntryTypeFile != 1 || EntryTypeDirectory != 2 || EntryTypeSymlink != 3 {
		t.Fatalf("unexpected EntryType values: %d %d %d %d",
			EntryTypeUnspecified, EntryTypeFile, EntryTypeDirectory, EntryTypeSymlink)
	}
	if EntryTypeFile.String() != "file" ||
		EntryTypeDirectory.String() != "directory" ||
		EntryTypeSymlink.String() != "symlink" ||
		EntryTypeUnspecified.String() != "" {
		t.Fatalf("unexpected EntryType.String() values: %q %q %q %q",
			EntryTypeFile, EntryTypeDirectory, EntryTypeSymlink, EntryTypeUnspecified)
	}
}

func TestFilesystemEventTypeConstants(t *testing.T) {
	if FsEventUnspecified == FsEventCreate ||
		FsEventCreate == FsEventWrite ||
		FsEventWrite == FsEventRemove {
		t.Fatal("FilesystemEventType constants must be distinct")
	}
}
