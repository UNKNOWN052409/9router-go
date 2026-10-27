#!/bin/bash
# History scrub filter for git filter-branch (run from the repo root).
# Replaces the leaked router key and personal paths in every file of every
# commit on the branch being rewritten.
for f in $(git ls-files); do
  [ -f "$f" ] || continue
  grep -qE "<your-router-key>|Users\.Unkno|Users\\\\Unkno" "$f" 2>/dev/null || continue
  sed -i \
    -e 's/<your-router-key>/<your-router-key>/g' \
    -e 's|C:\\\\Users\\\\Unkno|<user-profile>|g' \
    -e 's|C:\\Users\\Unkno|<user-profile>|g' \
    -e 's|<user-profile>|<user-profile>|g' \
    -e 's|<user-profile>|<user-profile>|g' \
    "$f"
done
exit 0
