#!/usr/bin/env bash
# Generated API tests of this module's VTN against the OpenADR 3.1.0 OpenAPI
# document, with Schemathesis. On demand and outside the normal gates: it never
# talks to anything but the disposable VTN it starts itself, on the loopback
# interface, with an in-memory store and made-up clients. See TESTING.md.
#
#   scripts/api-fuzz.sh [report dir]      needs go and uv; MAX_TIME=seconds
#
# It does not replace the conformance and authorization tests: Schemathesis
# knows the document, not the rules around it (which role may read what, which
# VEN sees which object).
#
# Subscriptions and notifiers stay out. Without them the VTN answers 501 by
# design; with them a generated callback URL would make it call that URL.
set -euo pipefail
export GOWORK=off

SCHEMATHESIS_VERSION=4.32.7
SPEC_URL=https://raw.githubusercontent.com/grid-coordination/openadr3-specification/ca9696d324b8ab6a41b34aaefa48fc98c0a775f5/3.1.0/openadr3.yaml
SPEC_SHA256=135bc74af2f733338b55afa95df3cb3a1185867e8e41a7d16cdb2dbfaeb3b475
ADDR=127.0.0.1:${PORT:-18080}
BL_SECRET=api-fuzz-bl-$RANDOM$RANDOM
VEN_SECRET=api-fuzz-ven-$RANDOM$RANDOM
: "${MAX_TIME:=120}"

root=$(cd "$(dirname "$0")/.." && pwd)
report=$(mkdir -p "${1:-$root/bin/api-fuzz}" && cd "${1:-$root/bin/api-fuzz}" && pwd)
work=$(mktemp -d)
vtn_pid=
cleanup() {
  [ -z "$vtn_pid" ] || kill "$vtn_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

curl --fail --silent --show-error --location --retry 3 --output "$work/openadr3.yaml" "$SPEC_URL"
got=$(python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$work/openadr3.yaml")
[ "$got" = "$SPEC_SHA256" ] || { echo "OpenAPI document has SHA-256 $got, want $SPEC_SHA256" >&2; exit 2; }

(cd "$root" && go build -o "$work/vtn" ./examples/vtn)
# Memory for the fake objects is what a run can use up; the rate limit is the VTN's own.
(
  ulimit -v 4194304 2>/dev/null || true
  exec "$work/vtn" -addr "$ADDR" -rate 200 -client "fuzz-bl:$BL_SECRET:bl" -client "fuzz-ven:$VEN_SECRET:ven"
) > "$report/vtn.log" 2>&1 &
vtn_pid=$!
for _ in $(seq 1 50); do
  curl --silent --output /dev/null "http://$ADDR/auth/server" && break
  sleep 0.1
done

token() {
  curl --fail --silent --show-error --user "$1:$2" --data grant_type=client_credentials "http://$ADDR/auth/token" |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])'
}
BL_TOKEN=$(token fuzz-bl "$BL_SECRET")

rc=0
cd "$work" # Schemathesis leaves a cache directory where it runs.
# --url overrides the servers of the document, so nothing leaves the machine.
NO_PROXY='*' uvx --python 3.13 --from "schemathesis==${SCHEMATHESIS_VERSION}" schemathesis run "$work/openadr3.yaml" \
  --url "http://$ADDR" \
  --header "Authorization: Bearer $BL_TOKEN" \
  --seed "${SEED:-20261101}" \
  --max-time "$MAX_TIME" \
  --workers 1 \
  --rate-limit 50/s \
  --request-timeout 10 \
  --max-redirects 0 \
  --exclude-path-regex '^/(subscriptions|notifiers)' \
  --exclude-checks unsupported_method \
  --report json,junit \
  --report-dir "$report" \
  --output-sanitize true \
  --warnings off \
  --no-color || rc=$?
echo "report in $report (exit $rc)"
exit "$rc"
