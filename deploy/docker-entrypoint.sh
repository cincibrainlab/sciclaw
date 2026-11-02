#!/bin/sh
set -eu

for skill in gitea-create-change-request gitea-move-work-item gitea-move-file-phase-0-to-1; do
  if [ -d "/opt/sciclaw/skills/$skill" ]; then
    mkdir -p /root/sciclaw/skills
    if [ "$skill" = gitea-move-file-phase-0-to-1 ]; then
      rm -rf /root/sciclaw/skills/move-task-phase-0-to-1
    fi
    rm -rf "/root/sciclaw/skills/$skill"
    cp -r "/opt/sciclaw/skills/$skill" "/root/sciclaw/skills/$skill"
  fi
done

exec sciclaw "$@"
