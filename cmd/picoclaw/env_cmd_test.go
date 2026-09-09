package main

import (
	"slices"
	"testing"
)

func TestTuiCmdRunsLocalAppTUI(t *testing.T) {
	orig := runAppTUI
	t.Cleanup(func() { runAppTUI = orig })

	called := false
	runAppTUI = func() { called = true }
	tuiCmd()
	if !called {
		t.Fatal("expected tuiCmd to invoke local app launcher")
	}
}

func TestDockerGatewayArgsMountSeparateRoots(t *testing.T) {
	args := dockerGatewayArgs("example/sciclaw:test", "/host/sciclaw", "/host/.sciclaw")
	for _, mount := range []string{
		"/host/sciclaw:/root/sciclaw",
		"/host/.sciclaw:/root/.sciclaw",
	} {
		if !slices.Contains(args, mount) {
			t.Fatalf("missing mount %q in %#v", mount, args)
		}
	}
	for _, arg := range args {
		if arg == "/host/.picoclaw:/root/.picoclaw" {
			t.Fatalf("unexpected PicoClaw mount in %#v", args)
		}
	}
}

func TestPackagedVMHelperRootsUseSciClawShareOnly(t *testing.T) {
	roots := packagedVMHelperRoots("/opt/homebrew/bin/sciclaw")
	if len(roots) != 1 || roots[0] != "/opt/homebrew/share/sciclaw/deploy" {
		t.Fatalf("unexpected packaged helper roots: %#v", roots)
	}
}
