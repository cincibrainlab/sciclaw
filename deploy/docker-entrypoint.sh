#!/bin/sh
set -eu

mkdir -p /root/sciclaw/skills
for skill in gitea-create-change-request gitea-move-work-item gitea-move-file-phase-0-to-1; do
  if mkdir "/root/sciclaw/skills/$skill" 2>/dev/null; then
    (
      tmp=
      trap '[ -z "$tmp" ] || rm -f "$tmp"; rmdir "/root/sciclaw/skills/$skill" 2>/dev/null || :' 0
      tmp=$(mktemp "/root/sciclaw/skills/$skill/.SKILL.md.XXXXXX")
      cp "/opt/sciclaw/skills/$skill/SKILL.md" "$tmp"
      chmod 644 "$tmp"
      ln "$tmp" "/root/sciclaw/skills/$skill/SKILL.md" 2>/dev/null ||
        [ -e "/root/sciclaw/skills/$skill/SKILL.md" ] || [ -L "/root/sciclaw/skills/$skill/SKILL.md" ]
    )
  elif [ ! -e "/root/sciclaw/skills/$skill" ] && [ ! -L "/root/sciclaw/skills/$skill" ]; then
    echo "Cannot create workspace skill $skill" >&2
    exit 1
  elif ! cmp -s "/root/sciclaw/skills/$skill/SKILL.md" "/opt/sciclaw/skills/$skill/SKILL.md"; then
    echo "Warning: workspace skill $skill differs from the bundled version and may use obsolete MCP tools. Review /root/sciclaw/skills/$skill/SKILL.md against /opt/sciclaw/skills/$skill/SKILL.md and update it while preserving customizations." >&2
  fi
done

if [ -e /root/sciclaw/skills/move-task-phase-0-to-1 ] || [ -L /root/sciclaw/skills/move-task-phase-0-to-1 ]; then
  echo "Warning: legacy workspace skill move-task-phase-0-to-1 may use obsolete MCP tools. Review and update or retire it; it has been preserved." >&2
fi

exec sciclaw "$@"
