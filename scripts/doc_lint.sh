#!/usr/bin/env bash
# doc_lint.sh — Verifies that manifest keys documented in README.md and SKILL.md
# match the parser's canonical top-level key whitelist (pkg/manifest/parser.go).
# Catches doc drift like `include:` vs `includes:` before it ships.
set -euo pipefail
cd "$(dirname "$0")/.."

PARSER="pkg/manifest/parser.go"
DOCS=("README.md" "SKILL.md")

# Keys that legitimately appear at column 0 in doc examples but are
# route/step-level (not top-level) manifest keys.
SUBKEY_ALLOWLIST="if on steps action emit parallel on_failure timeout_sec table"

# 1. Extract the canonical top-level key whitelist from the parser.
VALID_KEYS=$(sed -n 's/^.*validKeys := \[\]string{\(.*\)}/\1/p' "$PARSER" \
  | tr -d '"' | tr ',' '\n' | sed 's/ //g' | sed '/^$/d')
if [ -z "$VALID_KEYS" ]; then
  echo "doc-lint: could not extract validKeys from $PARSER" >&2
  exit 2
fi

# 2. Collect top-level manifest keys from manifest-looking code fences
#    (untagged or yaml-tagged). Other languages are skipped.
documented_keys() {
  local file="$1" in_fence=0 tag=""
  while IFS= read -r line; do
    case "$line" in
      '```'*)
        if [ $in_fence -eq 0 ]; then
          in_fence=1
          tag="${line#\`\`\`}"
        else
          in_fence=0
          tag=""
        fi
        continue
        ;;
    esac
    # Only scan untagged fences (.spine manifests) and yaml-tagged fences.
    if [ $in_fence -eq 1 ] && { [ -z "$tag" ] || [ "$tag" = "yaml" ]; }; then
      case "$line" in
        [a-z_]*) # candidate top-level key at column 0
          key="${line%%:*}"
          case "$key" in
            *' '*) ;;                       # not a plain key line
            http|https|ws|wss) ;;           # protocol false positives
            '') ;;
            *[!a-z_]*) ;;                  # not a bare identifier (e.g. "spine/")
            *) echo "$key" ;;
          esac
          ;;
      esac
    fi
  done < "$file"
}

# 3. Compare documented keys against the whitelist.
exit_code=0
for doc in "${DOCS[@]}"; do
  [ -f "$doc" ] || continue
  while IFS= read -r key; do
    if printf '%s\n' $SUBKEY_ALLOWLIST | grep -qx -- "$key"; then
      continue # route/step-level key, shown bare in condition examples
    fi
    if ! printf '%s\n' "$VALID_KEYS" | grep -qx -- "$key"; then
      echo "doc-lint: $doc documents unknown top-level manifest key '$key'"
      echo "          valid keys: $(printf '%s ' $VALID_KEYS)"
      exit_code=1
    fi
  done < <(documented_keys "$doc" | sort -u)
done

if [ $exit_code -eq 0 ]; then
  echo "doc-lint: OK — all documented manifest keys match the parser whitelist"
fi
exit $exit_code
