package service

import (
	"fmt"
	"html"
	"strings"
)

func renderSystemdUnit(description, exePath string, args []string, pathEnv, userHome, stateHome string) string {
	command := strings.TrimSpace(strings.Join(append([]string{exePath}, args...), " "))
	return fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=always
RestartSec=3
Environment=HOME=%%h
Environment=PATH=%s
Environment="SCICLAW_HOME=%s"
Environment="SCICLAW_STATE_HOME=%s"
WorkingDirectory=%%h

[Install]
WantedBy=default.target
`, description, command, pathEnv, escapeSystemdEnvironment(userHome), escapeSystemdEnvironment(stateHome))
}

func renderLaunchdPlist(label, exePath string, args []string, stdoutPath, stderrPath, pathEnv, userHome, stateHome string) string {
	var programArgs strings.Builder
	programArgs.WriteString("    <string>" + exePath + "</string>\n")
	for _, arg := range args {
		programArgs.WriteString("    <string>" + arg + "</string>\n")
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>

  <key>ProgramArguments</key>
  <array>
%s  </array>

  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <true/>

  <key>StandardOutPath</key>
  <string>%s</string>

  <key>StandardErrorPath</key>
  <string>%s</string>

  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>%s</string>
    <key>SCICLAW_HOME</key>
    <string>%s</string>
    <key>SCICLAW_STATE_HOME</key>
    <string>%s</string>
  </dict>
</dict>
</plist>
`, label, programArgs.String(), stdoutPath, stderrPath, pathEnv, html.EscapeString(userHome), html.EscapeString(stateHome))
}

func escapeSystemdEnvironment(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
