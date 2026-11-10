#!/usr/bin/env bash
# Wait until the container rollout `wrangler deploy` started has finished:
# no rollout active on the application, and every container instance on its
# image. deploy.yml runs it between the deploy and smoke.sh, so the smoke
# test's /v1/info is the new container's, not the old one's (#127). It
# prints a line whenever what it sees changes and exits 1 at the deadline.
#
#   deploy/cloudflare/rollout-wait.sh <application> [timeout seconds]
#
# The timeout defaults to 20 minutes: a replaced instance has 15 to exit
# after SIGTERM, then its successor starts. A deploy that changes nothing
# in the container starts no rollout, and this returns at once. Needs
# CLOUDFLARE_API_TOKEN (Workers Containers read) and CLOUDFLARE_ACCOUNT_ID.
set -euo pipefail
name=${1:?usage: rollout-wait.sh <application> [timeout seconds]}
timeout=${2:-1200}
api=https://api.cloudflare.com/client/v4/accounts/$CLOUDFLARE_ACCOUNT_ID/containers/applications

get() { curl -sS --fail-with-body --max-time 30 -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" "$api$1"; }

id=$(get "" | jq -r --arg name "$name" '.result[] | select(.name == $name) | .id')
[[ -n $id ]] || { echo "rollout-wait: no container application $name" >&2; exit 1; }
deadline=$((SECONDS + timeout))
last=
while :; do
	app=$(get "/$id" | jq -c .result)
	rollout=$(jq -r '.active_rollout_id // empty' <<<"$app")
	state="version $(jq -r .version <<<"$app")"
	if [[ -n $rollout ]]; then
		state+=", rollout ${rollout:0:8} $(get "/$id/rollouts/$rollout" | jq -r '.result | "\(.status), \(.progress | tojson)"')"
	fi
	# Instances on another image than the application's: the old ones.
	stale=$(get "/$id/instances" | jq --arg image "$(jq -r .configuration.image <<<"$app")" \
		'[(.result.instances // [])[] | select(.image != $image)] | length')
	state+=", $stale instance(s) on an older image"
	[[ $state == "$last" ]] || printf '%s  %s\n' "$(date -u +%TZ)" "$state"
	last=$state
	[[ -z $rollout && $stale == 0 ]] && exit 0
	((SECONDS < deadline)) || { echo "rollout-wait: $name not rolled out after ${timeout}s" >&2; exit 1; }
	sleep 10
done
