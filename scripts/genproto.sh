#!/bin/sh
# Regenerates the Go code for proto/pao/mesh/v1 into internal/mesh/meshpb.
# The generated files are committed; CI regenerates them and fails if they differ.
# The tool versions are pinned here and in .github/workflows/ci.yml, and a
# different version is refused, so the output does not depend on whose machine
# generated it.
set -eu

PROTOC_VERSION="36.2"
PROTOC_GEN_GO_VERSION="1.36.12"
PROTOC_GEN_GO_GRPC_VERSION="1.6.2"

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

check() {
  want="$1"; got="$2"; what="$3"
  if [ "$got" != "$want" ]; then
    echo "genproto: $what is $got, want $want (see scripts/genproto.sh)" >&2
    exit 1
  fi
}

check "libprotoc $PROTOC_VERSION" "$(protoc --version)" "protoc"
check "protoc-gen-go v$PROTOC_GEN_GO_VERSION" "$(protoc-gen-go --version)" "protoc-gen-go"
check "protoc-gen-go-grpc $PROTOC_GEN_GO_GRPC_VERSION" "$(protoc-gen-go-grpc --version)" "protoc-gen-go-grpc"

module="github.com/yaad-index/personal-agent-orchestrator"
protoc --proto_path=proto \
  --go_out=. --go_opt=module="$module" \
  --go-grpc_out=. --go-grpc_opt=module="$module" \
  proto/pao/mesh/v1/mesh.proto
