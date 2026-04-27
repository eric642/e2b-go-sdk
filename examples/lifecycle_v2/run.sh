#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=/dev/null
source ./.env
go run ./examples/lifecycle_v2
