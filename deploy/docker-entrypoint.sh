#!/bin/sh
set -eu

rm -rf /root/sciclaw/skills/move-task-phase-0-to-1 /root/sciclaw/skills/gitea-create-change-request /root/sciclaw/skills/gitea-move-work-item /root/sciclaw/skills/gitea-move-file-phase-0-to-1 /root/sciclaw/skills/atomic-gene-therapy
cp -r /opt/sciclaw/skills/atomic-gene-therapy /root/sciclaw/skills/atomic-gene-therapy

exec sciclaw "$@"
