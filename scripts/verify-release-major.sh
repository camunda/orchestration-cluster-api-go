#!/usr/bin/env bash
# Fails unless the major of the version about to be released agrees with the
# module path in go.mod. Go requires majors >= 2 to carry a matching `/vN`
# suffix, so a mismatched tag would ship a version `go get` cannot resolve.
#
# Usage: scripts/verify-release-major.sh <version>   (e.g. 10.0.1, 11.0.0-alpha.2)
set -euo pipefail

version="${1:?usage: $0 <version>}"
version="${version#v}"
module="$(awk '$1 == "module" { print $2; exit }' "$(dirname "$0")/../go.mod")"
if [ -z "$module" ]; then
  echo "::error::Could not read the module path from go.mod." >&2
  exit 1
fi

major="${version%%.*}"
if ! [[ "$major" =~ ^[0-9]+$ ]]; then
  echo "::error::'$1' is not a SemVer version." >&2
  exit 1
fi

suffix_major=""
if [[ "$module" =~ /v([0-9]+)$ ]]; then
  suffix_major="${BASH_REMATCH[1]}"
fi

echo "version=$version module=$module major=$major suffix_major=${suffix_major:-<none>}"
if [ "$major" -ge 2 ]; then
  if [ "$suffix_major" != "$major" ]; then
    echo "::error::Version $version has major $major but the module path is '$module'. Go requires a matching '/v$major' suffix on the module path before releasing this major." >&2
    exit 1
  fi
elif [ -n "$suffix_major" ]; then
  echo "::error::Version $version has major $major but the module path carries a '/v$suffix_major' suffix; drop the suffix or release a matching major." >&2
  exit 1
fi
