package heartbeat

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	stateHome, err := os.MkdirTemp("", "sciclaw-heartbeat-state-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("SCICLAW_STATE_HOME", stateHome)
	code := m.Run()
	_ = os.RemoveAll(stateHome)
	os.Exit(code)
}
