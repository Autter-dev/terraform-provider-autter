#!/usr/bin/env bash
# Copy the files this provider tests against from a checkout of the Autter
# monorepo: the OpenAPI document of /api/v1 and the "Export as Terraform"
# fixtures (the HCL the export writes, and the API resources it was written
# from). Run it after the API's schemas or the export change, then run the
# tests: openapi_test.go and export_contract_test.go fail when the provider no
# longer matches.
#
#   scripts/sync-from-monorepo.sh ../autter-monorepo
set -euo pipefail

monorepo="${1:?usage: $0 <path to the autter monorepo checkout>}"
here="$(cd "$(dirname "$0")/.." && pwd)"

openapi="$monorepo/docs/openapi/autter-v1.yaml"
fixtures="$monorepo/backend/src/routes/public-api/testdata/terraform-export"
[ -f "$openapi" ] || { echo "not found: $openapi" >&2; exit 1; }
[ -d "$fixtures" ] || { echo "not found: $fixtures" >&2; exit 1; }

cp "$openapi" "$here/internal/provider/testdata/autter-v1.yaml"
mkdir -p "$here/internal/provider/testdata/export"
rm -f "$here"/internal/provider/testdata/export/*.tf "$here"/internal/provider/testdata/export/*.json
cp "$fixtures"/*.tf "$fixtures"/*.json "$here/internal/provider/testdata/export/"

commit="$(git -C "$monorepo" rev-parse --short HEAD 2>/dev/null || echo unknown)"
echo "monorepo $commit" > "$here/internal/provider/testdata/SOURCE"
echo "Synced from $monorepo at $commit."
