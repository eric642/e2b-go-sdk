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
