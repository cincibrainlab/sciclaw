#!/bin/sh
set -eu

mkdir -p /root/sciclaw/skills
for skill in gitea-create-change-request gitea-move-work-item gitea-move-file-phase-0-to-1; do
  dir="/root/sciclaw/skills/$skill"
  mkdir -p "$dir"
  if [ ! -e "$dir/SKILL.md" ] && [ ! -L "$dir/SKILL.md" ]; then
    (
      tmp=$(mktemp "$dir/.SKILL.md.XXXXXX")
      trap 'rm -f "$tmp"' 0
      cp "/opt/sciclaw/skills/$skill/SKILL.md" "$tmp"
      chmod 644 "$tmp"
      ln "$tmp" "$dir/SKILL.md" 2>/dev/null ||
        [ -e "$dir/SKILL.md" ] || [ -L "$dir/SKILL.md" ]
    )
  fi
  if ! cmp -s "$dir/SKILL.md" "/opt/sciclaw/skills/$skill/SKILL.md"; then
    echo "Warning: workspace skill $skill differs from the bundled version and may use obsolete MCP tools. Review $dir/SKILL.md against /opt/sciclaw/skills/$skill/SKILL.md and update it while preserving customizations." >&2
  fi
done

if [ -e /root/sciclaw/skills/move-task-phase-0-to-1 ] || [ -L /root/sciclaw/skills/move-task-phase-0-to-1 ]; then
  echo "Warning: legacy workspace skill move-task-phase-0-to-1 may use obsolete MCP tools. Review and update or retire it; it has been preserved." >&2
fi

exec sciclaw "$@"
