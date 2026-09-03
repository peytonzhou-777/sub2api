#!/bin/sh
set -eu

image=${1:?usage: verify-runtime-image.sh <image> [platform] [expected-revision]}
platform=${2:-linux/amd64}
expected_revision=${3:-}

# 发布镜像必须同时携带服务和停机迁移工具，避免源码产物与镜像内容漂移。
docker pull --platform "$platform" "$image"
docker run --rm --platform "$platform" --entrypoint /bin/sh "$image" -ec '
  test -x /app/sub2api
  test -x /app/openai-persona-migrate
  /app/openai-persona-migrate --help >/dev/null
'

if [ -n "$expected_revision" ]; then
  actual_revision=$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image")
  [ "$actual_revision" = "$expected_revision" ] || {
    printf 'runtime image revision mismatch: expected %s, got %s\n' "$expected_revision" "$actual_revision" >&2
    exit 1
  }
fi

printf 'runtime image contract verified: %s (%s)\n' "$image" "$platform"
