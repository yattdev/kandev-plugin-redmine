#!/usr/bin/env bash
# Validates the SDK release before a plugin release can change metadata or refs.
set -euo pipefail

: "${SDK_REPOSITORY:?SDK_REPOSITORY is required}"
: "${SDK_REF:?SDK_REF is required}"
: "${REQUIRED_CONTRACT_COMMIT:?REQUIRED_CONTRACT_COMMIT is required}"

readonly CANONICAL_REPOSITORY="kdlbs/kandev"
readonly SDK_URL="https://github.com/${CANONICAL_REPOSITORY}.git"
readonly GIT_BIN="${GIT_BIN:-git}"

fail() {
  echo "Release blocked: $*" >&2
  exit 1
}

test "$SDK_REPOSITORY" = "$CANONICAL_REPOSITORY" || \
  fail "SDK contract must use ${CANONICAL_REPOSITORY}, got ${SDK_REPOSITORY}@${SDK_REF}."

[[ "$SDK_REF" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || \
  fail "SDK ref must be an upstream SemVer release tag, got ${SDK_REF}."

"$GIT_BIN" ls-remote --exit-code --refs "$SDK_URL" "refs/tags/${SDK_REF}" >/dev/null 2>&1 || \
  fail "SDK release tag ${SDK_REF} does not exist in ${CANONICAL_REPOSITORY}."

sdk_checkout="$(mktemp -d)"
cleanup() { rm -rf "$sdk_checkout"; }
trap cleanup EXIT

"$GIT_BIN" -C "$sdk_checkout" init --quiet
"$GIT_BIN" -C "$sdk_checkout" fetch --quiet "$SDK_URL" \
  "refs/tags/${SDK_REF}:refs/tags/${SDK_REF}" \
  "${REQUIRED_CONTRACT_COMMIT}:refs/kandev-required-contract"

"$GIT_BIN" -C "$sdk_checkout" merge-base --is-ancestor \
  "$REQUIRED_CONTRACT_COMMIT" "refs/tags/${SDK_REF}" || \
  fail "SDK release ${SDK_REF} does not contain required contract ${REQUIRED_CONTRACT_COMMIT}."

echo "Validated ${CANONICAL_REPOSITORY}@${SDK_REF} contains ${REQUIRED_CONTRACT_COMMIT}."
