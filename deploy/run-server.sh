#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
set -a
source /etc/pangu-sales-manager.env
source /etc/pangu-sales-manager-marketing.env
set +a
exec ./bin/pangu-sales-manager
