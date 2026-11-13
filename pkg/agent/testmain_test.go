package agent

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	testHome, err := os.MkdirTemp("", "sciclaw-agent-home-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("SCICLAW_HOME", testHome+"/user")
	_ = os.Setenv("SCICLAW_STATE_HOME", testHome+"/state")
	code := m.Run()
	_ = os.RemoveAll(testHome)
	os.Exit(code)
}
