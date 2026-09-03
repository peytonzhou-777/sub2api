#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

fail() {
  printf 'docker runtime resources test failed: %s\n' "$1" >&2
  exit 1
}

assert_line() {
  file=$1
  line=$2
  grep -Fqx "$line" "$file" || fail "$file is missing: $line"
}

assert_count() {
  file=$1
  line=$2
  expected=$3
  actual=$(grep -Fxc "$line" "$file" || true)
  [ "$actual" -eq "$expected" ] || fail "$file has $actual occurrences of '$line', expected $expected"
}

test -s backend/resources/model-pricing/model_prices_and_context_window.json || \
  fail 'fallback pricing data is missing or empty'

assert_line Dockerfile.goreleaser 'COPY --chown=sub2api:sub2api backend/resources /app/resources'
assert_line Dockerfile 'COPY --from=backend-builder --chown=sub2api:sub2api /app/backend/resources /app/resources'
assert_count .goreleaser.yaml '      - backend/resources' 4
assert_count .goreleaser.simple.yaml '      - backend/resources' 1

# 运行时镜像契约由两个生产构建入口共同满足；deploy 下不再保留源码构建副本。
assert_line Dockerfile '    -o /app/openai-persona-migrate \'
assert_line Dockerfile 'COPY --from=backend-builder --chown=sub2api:sub2api /app/openai-persona-migrate /app/openai-persona-migrate'
assert_line Dockerfile.goreleaser 'COPY openai-persona-migrate /app/openai-persona-migrate'
assert_line backend/Dockerfile '    go build -ldflags="-s -w" -o /app/openai-persona-migrate ./cmd/openai-persona-migrate/'
assert_line .goreleaser.yaml '  - id: openai-persona-migrate'
assert_line .goreleaser.simple.yaml '  - id: openai-persona-migrate'
assert_count .goreleaser.yaml '      - openai-persona-migrate' 4
assert_count .goreleaser.simple.yaml '      - openai-persona-migrate' 1
assert_count .github/workflows/prebuild.yml '      - name: Verify published runtime image' 1
assert_count .github/workflows/release.yml '      - name: Verify published runtime image' 1
test ! -e deploy/Dockerfile || fail 'deploy/Dockerfile must not duplicate the canonical root Dockerfile'
/bin/sh -n deploy/verify-runtime-image.sh

printf 'docker runtime resources test passed\n'
