#!/usr/bin/env bash
# Sets up the backup Claude account's config dir for the daemon's quota failover
# (see PATCHES.md, "Backup Claude account failover, urgent only"). Safe to run again.
#
#   ag-patches/claude-backup-setup.sh
#
# Creates ~/.claude-backup (or $MULTICA_AG_CLAUDE_BACKUP_DIR), copies .claude.json
# into it once (it holds oauthAccount, so it is per account and never symlinked)
# and symlinks the shared parts of ~/.claude (or $MULTICA_AG_CLAUDE_PRIMARY_DIR)
# into it. A shared projects/ is what lets a session resume across accounts.
# Only items that exist in the primary are linked. A real file or directory, or a
# symlink to somewhere else, already in the backup is never overwritten: it is
# reported and the script exits 1 once the rest is done.
#
# No credentials are copied. Log the backup in separately; they go to their own
# macOS Keychain item keyed by a hash of the config dir.
set -euo pipefail

primary="${MULTICA_AG_CLAUDE_PRIMARY_DIR:-$HOME/.claude}"
backup="${MULTICA_AG_CLAUDE_BACKUP_DIR:-$HOME/.claude-backup}"

if [ ! -d "$primary" ]; then
  echo "error: primary config dir $primary does not exist; log in to Claude Code there first" >&2
  exit 1
fi
if [ "${primary%/}" = "${backup%/}" ]; then
  echo "error: the backup dir is the primary dir ($backup)" >&2
  exit 1
fi

mkdir -p "$backup"
chmod 700 "$backup"

conflicts=0

# .claude.json: the default account keeps it at ~/.claude.json, a custom
# CLAUDE_CONFIG_DIR keeps it inside the dir.
if [ -e "$backup/.claude.json" ] || [ -L "$backup/.claude.json" ]; then
  echo "kept   .claude.json (already in $backup)"
else
  src=""
  for candidate in "$primary/.claude.json" "$HOME/.claude.json"; do
    if [ -f "$candidate" ]; then src="$candidate"; break; fi
  done
  if [ -n "$src" ]; then
    cp "$src" "$backup/.claude.json"
    chmod 600 "$backup/.claude.json"
    echo "copied .claude.json from $src"
  else
    echo "skip   .claude.json (none found; Claude Code creates one at login)"
  fi
fi

link_item() {
  local name="$1" source="$primary/$1" target="$backup/$1"
  if [ ! -e "$source" ] && [ ! -L "$source" ]; then
    return 0
  fi
  if [ -L "$target" ]; then
    if [ "$(readlink "$target")" = "$source" ]; then
      echo "kept   $name"
    else
      echo "conflict: $target is a symlink to $(readlink "$target"), not $source; left alone" >&2
      conflicts=$((conflicts + 1))
    fi
  elif [ -e "$target" ]; then
    echo "conflict: $target already exists and is not a symlink; left alone" >&2
    conflicts=$((conflicts + 1))
  else
    ln -s "$source" "$target"
    echo "linked $name"
  fi
}

for name in settings.json CLAUDE.md keybindings.json plugins skills agents commands hooks \
  projects file-history todos shell-snapshots session-env plans; do
  link_item "$name"
done
shopt -s nullglob
for path in "$primary"/statusline.*; do
  link_item "$(basename "$path")"
done
shopt -u nullglob

if [ "$conflicts" -gt 0 ]; then
  echo "$conflicts item(s) left alone; fix them by hand and run this again" >&2
  exit 1
fi

cat <<MSG

Done. Now log the backup account in (use the other account, not the primary):

  CLAUDE_CONFIG_DIR=$backup claude auth login

then turn the backup on from the runtime page. The daemon checks it every 30 minutes.
MSG
