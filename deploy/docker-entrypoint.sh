#!/bin/sh
set -eu

rm -rf /root/sciclaw/skills/move-task-phase-0-to-1

for skill in gitea-create-change-request gitea-move-work-item gitea-move-file-phase-0-to-1; do
  rm -rf "/root/sciclaw/skills/$skill"
  cp -r "/opt/sciclaw/skills/$skill" "/root/sciclaw/skills/$skill"
done

exec sciclaw "$@"
