package template

import "testing"

func TestFromImage_NoCredsLeavesRegistryNil(t *testing.T) {
	b := New().FromImage("alpine:3")
	if b.registryConfig != nil {
		t.Fatal("registryConfig should be nil when no creds are provided")
	}
}

func TestFromImage_WithBasicCreds(t *testing.T) {
	b := New().FromImage("priv/x:latest",
		RegistryCredentials{Username: "u", Password: "p"})
	if b.registryConfig == nil {
		t.Fatal("no registry config stored")
	}
}

func TestFromImage_EmptyCredsLeavesRegistryNil(t *testing.T) {
	b := New().FromImage("priv/x:latest", RegistryCredentials{})
	if b.registryConfig != nil {
		t.Fatal("registryConfig should stay nil when creds struct is empty")
	}
}

func TestFromAWSRegistry(t *testing.T) {
	b := New().FromAWSRegistry(
		"123.dkr.ecr.us-west-2.amazonaws.com/x:latest",
		"AKIA", "SECRET", "us-west-2",
	)
	if b.baseImage != "123.dkr.ecr.us-west-2.amazonaws.com/x:latest" {
		t.Fatalf("baseImage: %q", b.baseImage)
	}
	if b.baseTemplate != "" {
		t.Fatalf("baseTemplate should be cleared: %q", b.baseTemplate)
	}
	if b.registryConfig == nil {
		t.Fatal("registryConfig should be set for AWS")
	}
}

func TestFromGCPRegistry(t *testing.T) {
	b := New().FromGCPRegistry("gcr.io/p/i:latest", `{"type":"sa"}`)
	if b.baseImage != "gcr.io/p/i:latest" {
		t.Fatalf("baseImage: %q", b.baseImage)
	}
	if b.registryConfig == nil {
		t.Fatal("registryConfig should be set for GCP")
	}
}

func TestFromAWSRegistry_SerializesIntoBody(t *testing.T) {
	b := New().FromAWSRegistry("img:latest", "AKIA", "SECRET", "us-west-2")
	body, err := b.serialize(false)
	if err != nil {
		t.Fatal(err)
	}
	if body.FromImageRegistry == nil {
		t.Fatal("serialize() did not propagate registryConfig")
	}
}
