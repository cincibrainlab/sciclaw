package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_AGENT_CMD_JSON_HELPER") == "1" || os.Getenv("GO_WANT_AGENT_CMD_RETRY_HELPER") == "1" {
		os.Exit(m.Run())
	}
	stateHome, err := os.MkdirTemp("", "sciclaw-command-state-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("SCICLAW_STATE_HOME", stateHome)
	code := m.Run()
	_ = os.RemoveAll(stateHome)
	os.Exit(code)
}
