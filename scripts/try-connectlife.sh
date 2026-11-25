#!/usr/bin/env bash
# Copyright (c) 2026 InfAI (CC SES)
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Runs the connector functions against the real ConnectLife cloud, read-only:
# which appliances exist, what they report, which device types would be created.
#
# Credentials are asked once and stored in .local/connectlife.env (mode 600).
# .local/ is in .gitignore; the script refuses to run if git would track it.
#
#   scripts/try-connectlife.sh            run with stored credentials
#   scripts/try-connectlife.sh --reset    ask for credentials again
#   SENERGY_PROFILE=jonah scripts/try-connectlife.sh
#                                         also look up existing device types on
#                                         api.senergy.infai.org via credctl (default jonah,
#                                         set SENERGY_PROFILE= to skip the lookup)

set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
local_dir="$repo/.local"
cred_file="$local_dir/connectlife.env"
out_dir="$local_dir/out/$(date +%Y%m%d-%H%M%S)"
cli="$local_dir/bin/connectlife-cli"
platform_api="https://api.senergy.infai.org"
profile="${SENERGY_PROFILE-jonah}"

cd "$repo"
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

# never store credentials in a tracked or trackable place
if ! git check-ignore -q "$cred_file"; then
	echo "$cred_file is not ignored by git, aborting" >&2
	exit 1
fi

ask_credentials() {
	local username password
	read -r -p "ConnectLife e-mail: " username
	read -r -s -p "ConnectLife password: " password; echo
	mkdir -p "$local_dir"
	chmod 700 "$local_dir"
	(
		umask 077
		{
			printf 'CONNECTLIFE_USERNAME=%q\n' "$username"
			printf 'CONNECTLIFE_PASSWORD=%q\n' "$password"
		} > "$cred_file"
	)
	echo "stored in $cred_file"
}

if [[ "${1:-}" == "--reset" || ! -f "$cred_file" ]]; then
	ask_credentials
fi
set -a
# shellcheck disable=SC1090
source "$cred_file"
set +a

mkdir -p "$local_dir/bin" "$out_dir"
go build -o "$cli" ./cmd/connectlife-cli

section() { printf '\n== %s\n' "$1"; }

section "Login"
"$cli" login

section "Appliances"
"$cli" devices
"$cli" -json devices > "$out_dir/devices.json"

for id in $(jq -r '.[].device_id' "$out_dir/devices.json"); do
	name="$(jq -r --arg id "$id" '.[] | select(.device_id == $id) | "\(.name) (\(.type_name), \(.type_key))"' "$out_dir/devices.json")"
	section "Properties of $id: $name"
	"$cli" properties "$id"
	"$cli" -json properties "$id" > "$out_dir/$id.properties.json"
	"$cli" status "$id" > "$out_dir/$id.status.json"
	"$cli" devicetype "$id" > "$out_dir/$id.devicetype.json" || true
	# what the cloud knows about the model, e.g. which properties are writable
	if "$cli" property-list "$id" > "$out_dir/$id.property-list.json" 2> "$out_dir/$id.property-list.err"; then
		echo "property metadata of the model: $out_dir/$id.property-list.json"
	else
		echo "property metadata not available: $(cat "$out_dir/$id.property-list.err")"
	fi
done

section "Device types"
"$cli" -json plan > "$out_dir/plan.json"
if [[ -n "$profile" ]] && command -v credctl >/dev/null; then
	for key in $(jq -r '.[].type_key' "$out_dir/plan.json"); do
		found="$(credctl run "$profile" -- curl -sf -G "$platform_api/device-repository/v3/device-types" \
			--data-urlencode "attr-keys=connectlife/type" \
			--data-urlencode "attr-values=$key" \
			| jq -r --arg key "$key" '[.[] | select(any(.attributes[]?; .key == "connectlife/type" and .value == $key)) | .id] | sort | first // empty')" \
			|| found="lookup failed"
		jq --arg key "$key" --arg found "$found" '
			map(if .type_key == $key and .action != "skip" then
				if $found == "" then .action = "would create"
				elif $found == "lookup failed" then .action = "unknown (platform lookup failed)"
				else .action = "use existing" | .device_type_id = $found end
			else . end)' "$out_dir/plan.json" > "$out_dir/plan.tmp" && mv "$out_dir/plan.tmp" "$out_dir/plan.json"
	done
else
	echo "no platform lookup (credctl missing or SENERGY_PROFILE empty)"
fi
jq -r '.[] | "\(.type_key)\n  device type: \(.device_type_name // "-")\n  properties:  \(.properties) (\(.writable_properties) writable)\n  skipped:     \(if (.skipped_properties | length) > 0 then (.skipped_properties | join(", ")) else "-" end)\n  action:      \(.action)\(if .device_type_id then " " + .device_type_id else "" end)\(if .error then ": " + .error else "" end)\n  devices:     \(.devices | join(", "))"' "$out_dir/plan.json"

section "Output"
echo "$out_dir"
echo "Generated device types: $out_dir/<device>.devicetype.json"
echo "Nothing was written to the appliances or the platform."
