package template

import "testing"

func TestPipInstall_Global(t *testing.T) {
	b := New().PipInstall([]string{"numpy"})
	in := b.instructions[0]
	if in.Args[0] != "pip install numpy" || in.Args[1] != "root" {
		t.Fatalf("args: %v", in.Args)
	}
}

func TestPipInstall_UserInstall(t *testing.T) {
	b := New().PipInstall([]string{"numpy"}, WithPipUserInstall())
	in := b.instructions[0]
	if in.Args[0] != "pip install --user numpy" {
		t.Fatalf("cmd: %q", in.Args[0])
	}
	if len(in.Args) != 1 {
		t.Fatalf("user should be empty; args: %v", in.Args)
	}
}

func TestPipInstall_NoPackages(t *testing.T) {
	b := New().PipInstall(nil)
	if b.instructions[0].Args[0] != "pip install ." {
		t.Fatalf("cmd: %q", b.instructions[0].Args[0])
	}
}

func TestNpmInstall_Global(t *testing.T) {
	b := New().NpmInstall([]string{"typescript"}, WithNpmGlobal())
	in := b.instructions[0]
	if in.Args[0] != "npm install -g typescript" || in.Args[1] != "root" {
		t.Fatalf("args: %v", in.Args)
	}
}

func TestNpmInstall_Dev(t *testing.T) {
	b := New().NpmInstall([]string{"typescript"}, WithNpmDev())
	if b.instructions[0].Args[0] != "npm install --save-dev typescript" {
		t.Fatalf("cmd: %q", b.instructions[0].Args[0])
	}
}

func TestNpmInstall_NoPackages(t *testing.T) {
	b := New().NpmInstall(nil)
	if b.instructions[0].Args[0] != "npm install" {
		t.Fatalf("cmd: %q", b.instructions[0].Args[0])
	}
}

func TestBunInstall_Dev(t *testing.T) {
	b := New().BunInstall([]string{"tsx"}, WithBunDev())
	if b.instructions[0].Args[0] != "bun install --dev tsx" {
		t.Fatalf("cmd: %q", b.instructions[0].Args[0])
	}
}

func TestBunInstall_Global(t *testing.T) {
	b := New().BunInstall([]string{"tsx"}, WithBunGlobal())
	if b.instructions[0].Args[0] != "bun install -g tsx" || b.instructions[0].Args[1] != "root" {
		t.Fatalf("args: %v", b.instructions[0].Args)
	}
}

func TestAptInstall_Plain(t *testing.T) {
	b := New().AptInstall([]string{"vim"})
	want := "apt-get update && DEBIAN_FRONTEND=noninteractive DEBCONF_NOWARNINGS=yes apt-get install -y vim"
	if b.instructions[0].Args[0] != want {
		t.Fatalf("got %q\nwant %q", b.instructions[0].Args[0], want)
	}
	if b.instructions[0].Args[1] != "root" {
		t.Fatalf("apt should run as root: %v", b.instructions[0].Args)
	}
}

func TestAptInstall_FixMissing(t *testing.T) {
	b := New().AptInstall([]string{"vim"}, WithAptFixMissing())
	want := "apt-get update && DEBIAN_FRONTEND=noninteractive DEBCONF_NOWARNINGS=yes apt-get install -y --fix-missing vim"
	if b.instructions[0].Args[0] != want {
		t.Fatalf("got %q\nwant %q", b.instructions[0].Args[0], want)
	}
}

func TestAptInstall_BothFlags(t *testing.T) {
	b := New().AptInstall([]string{"git", "curl"}, WithAptNoInstallRecommends(), WithAptFixMissing())
	want := "apt-get update && DEBIAN_FRONTEND=noninteractive DEBCONF_NOWARNINGS=yes apt-get install -y --no-install-recommends --fix-missing git curl"
	if b.instructions[0].Args[0] != want {
		t.Fatalf("got %q\nwant %q", b.instructions[0].Args[0], want)
	}
}

func TestGitClone_BranchDepthPath(t *testing.T) {
	b := New().GitClone("https://x/y.git",
		WithGitBranch("main"), WithGitDepth(1), WithGitPath("/app"))
	want := "git clone https://x/y.git --branch main --single-branch --depth 1 /app"
	if b.instructions[0].Args[0] != want {
		t.Fatalf("got %q\nwant %q", b.instructions[0].Args[0], want)
	}
}

func TestGitClone_Plain(t *testing.T) {
	b := New().GitClone("https://x/y.git")
	if b.instructions[0].Args[0] != "git clone https://x/y.git" {
		t.Fatalf("got %q", b.instructions[0].Args[0])
	}
}

func TestGitClone_WithUser(t *testing.T) {
	b := New().GitClone("https://x/y.git", WithGitUser("root"))
	in := b.instructions[0]
	if in.Args[0] != "git clone https://x/y.git" || in.Args[1] != "root" {
		t.Fatalf("args: %v", in.Args)
	}
}
