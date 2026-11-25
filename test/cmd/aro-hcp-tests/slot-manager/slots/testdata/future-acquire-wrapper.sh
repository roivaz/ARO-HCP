#!/usr/bin/env bash
# Executable Phase B contract fixture only. This does not replace the release ref.
set -euo pipefail

args=(slot-manager acquire --deploy-env "${ARO_HCP_DEPLOY_ENV}" --shared-dir "${SHARED_DIR}")
selector="${MULTISTAGE_PARAM_OVERRIDE_POOL_SELECTOR:-${POOL_SELECTOR:-}}"
placement="${MULTISTAGE_PARAM_OVERRIDE_PLACEMENT_REGION:-${PLACEMENT_REGION:-}}"
subscriptions="${MULTISTAGE_PARAM_OVERRIDE_ALLOWED_SUBSCRIPTIONS:-${ALLOWED_SUBSCRIPTIONS:-}}"

if [[ -n "$selector" || -n "$placement" ]]; then
    if [[ -n "${ALLOWED_SUBSCRIPTIONS:-}" || -n "${MULTISTAGE_PARAM_OVERRIDE_ALLOWED_SUBSCRIPTIONS:-}" || -n "${ALLOWED_LOCATIONS:-}" ]]; then
        echo 'new controls cannot mix with legacy selectors' >&2
        exit 2
    fi
    if [[ -n "$placement" && -n "${MULTISTAGE_PARAM_OVERRIDE_LOCATION:-}" ]]; then
        echo 'duplicate concrete placement sources' >&2
        exit 2
    fi
    if [[ -n "${MULTISTAGE_PARAM_OVERRIDE_LOCATION:-}" ]]; then
        placement="$MULTISTAGE_PARAM_OVERRIDE_LOCATION"
        unset MULTISTAGE_PARAM_OVERRIDE_LOCATION
    fi
    if [[ -n "$selector" ]]; then args+=(--pool-selector "$selector"); fi
    if [[ -n "$placement" ]]; then args+=(--placement-region "$placement"); fi
else
    if [[ -n "$subscriptions" ]]; then args+=(--allowed-subscriptions "$subscriptions"); fi
    if [[ -n "${ALLOWED_LOCATIONS:-}" ]]; then args+=(--allowed-locations "$ALLOWED_LOCATIONS"); fi
fi
if [[ -n "${CLUSTER_PROFILE_DIRS:-}" ]]; then args+=(--cluster-profile-dirs "$CLUSTER_PROFILE_DIRS"); fi
exec ./test/aro-hcp-tests "${args[@]}"
