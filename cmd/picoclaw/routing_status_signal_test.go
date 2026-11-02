//go:build !windows

package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestGatewayRoutingStatusSIGTERM(t *testing.T) {
	if os.Getenv("SCICLAW_TEST_GATEWAY") == "1" {
		os.Args = []string{os.Args[0], "gateway"}
		gatewayCmd()
		return
	}
	home, err := os.MkdirTemp("", "rs-signal-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Providers.OpenAI.APIKey = "synthetic"
	cfg.Providers.OpenAI.APIBase = "http://127.0.0.1:1"
	if err := config.SaveConfig(filepath.Join(home, ".picoclaw", "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "status")
	for i := 0; i < 2; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestGatewayRoutingStatusSIGTERM$")
		cmd.Env = append(os.Environ(), "SCICLAW_TEST_GATEWAY=1", "HOME="+home, "INVOCATION_ID=synthetic", "SCICLAW_ROUTING_STATUS_DIR="+dir)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		deadline := time.NewTimer(15 * time.Second)
		ticker := time.NewTicker(25 * time.Millisecond)
		ready := false
		for !ready {
			select {
			case err := <-done:
				ticker.Stop()
				deadline.Stop()
				t.Fatalf("gateway exited before ready: %v\n%s", err, output.String())
			case <-deadline.C:
				cmd.Process.Kill()
				<-done
				ticker.Stop()
				t.Fatalf("gateway startup timeout\n%s", output.String())
			case <-ticker.C:
				conn, err := net.DialTimeout("unix", filepath.Join(dir, "status.sock"), time.Second)
				if err == nil {
					conn.Close()
					ready = true
				}
			}
		}
		ticker.Stop()
		deadline.Stop()
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			cmd.Process.Kill()
			<-done
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("gateway shutdown: %v\n%s", err, output.String())
			}
		case <-time.After(20 * time.Second):
			cmd.Process.Kill()
			<-done
			t.Fatalf("gateway shutdown timeout\n%s", output.String())
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("status directory remains after SIGTERM: %v", err)
		}
	}
}

func TestGatewaySIGTERMCleansStatusDuringBlockedChannelStartup(t *testing.T) {
	home, err := os.MkdirTemp("", "rs-block-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	entered, release := make(chan struct{}, 1), make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		http.Error(w, "synthetic proxy failure", http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Providers.OpenAI.APIKey = "synthetic"
	cfg.Channels.Discord.Enabled = true
	cfg.Channels.Discord.Token = "synthetic"
	if err := config.SaveConfig(filepath.Join(home, ".picoclaw", "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "status")
	cmd := exec.Command(os.Args[0], "-test.run=^TestGatewayRoutingStatusSIGTERM$")
	cmd.Env = append(os.Environ(), "SCICLAW_TEST_GATEWAY=1", "HOME="+home, "INVOCATION_ID=synthetic", "SCICLAW_ROUTING_STATUS_DIR="+dir, "HTTPS_PROXY="+proxy.URL, "HTTP_PROXY="+proxy.URL, "NO_PROXY=")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		close(release)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("channel startup did not reach proxy")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("status directory missing before SIGTERM: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("status directory was not removed while channel startup was blocked")
		case <-ticker.C:
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				return
			}
		}
	}
}
