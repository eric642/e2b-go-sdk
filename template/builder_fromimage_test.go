package template

import "testing"

func TestFromDebianImage_Default(t *testing.T) {
	if got := New().FromDebianImage("").BaseImage(); got != "debian:stable" {
		t.Fatalf("got %q", got)
	}
}

func TestFromDebianImage_Variant(t *testing.T) {
	if got := New().FromDebianImage("bookworm").BaseImage(); got != "debian:bookworm" {
		t.Fatalf("got %q", got)
	}
}

func TestFromUbuntuImage_Default(t *testing.T) {
	if got := New().FromUbuntuImage("").BaseImage(); got != "ubuntu:latest" {
		t.Fatalf("got %q", got)
	}
}

func TestFromUbuntuImage_Variant(t *testing.T) {
	if got := New().FromUbuntuImage("24.04").BaseImage(); got != "ubuntu:24.04" {
		t.Fatalf("got %q", got)
	}
}

func TestFromBunImage_Default(t *testing.T) {
	if got := New().FromBunImage("").BaseImage(); got != "oven/bun:latest" {
		t.Fatalf("got %q", got)
	}
}

func TestFromBunImage_Variant(t *testing.T) {
	if got := New().FromBunImage("1.1.0").BaseImage(); got != "oven/bun:1.1.0" {
		t.Fatalf("got %q", got)
	}
}

func TestFromFedoraAlpineArchImages(t *testing.T) {
	if got := New().FromFedoraImage("").BaseImage(); got != "fedora:44" {
		t.Fatalf("Fedora default: %q", got)
	}
	if got := New().FromFedoraImage("43").BaseImage(); got != "fedora:43" {
		t.Fatalf("Fedora variant: %q", got)
	}
	if got := New().FromAlpineImage("").BaseImage(); got != "alpine:3.24" {
		t.Fatalf("Alpine default: %q", got)
	}
	if got := New().FromAlpineImage("3.20").BaseImage(); got != "alpine:3.20" {
		t.Fatalf("Alpine variant: %q", got)
	}
	if got := New().FromArchImage("").BaseImage(); got != "archlinux:latest" {
		t.Fatalf("Arch default: %q", got)
	}
	if got := New().FromArchImage("base-devel").BaseImage(); got != "archlinux:base-devel" {
		t.Fatalf("Arch variant: %q", got)
	}
}
