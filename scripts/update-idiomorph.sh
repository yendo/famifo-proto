#!/bin/bash
# Re-fetch the idiomorph version declared in package.json and replace the bundled copy.
set -euo pipefail

cd "$(dirname "$0")/.."

version=$(jq -r .dependencies.idiomorph package.json)
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

curl -fsSL "https://cdn.jsdelivr.net/npm/idiomorph@$version/dist/idiomorph.esm.js" -o "$tmp"

# A CDN can answer 200 with an error page, and a cut transfer leaves a partial
# file, so only replace the bundled copy once the body ends with the module's
# export, which only a complete download has.
if ! tail -n 1 "$tmp" | grep -qx "export {Idiomorph};"; then
	echo "$0: the response is not idiomorph $version" >&2
	exit 1
fi

mv "$tmp" internal/web/static/idiomorph.esm.js
chmod 644 internal/web/static/idiomorph.esm.js
