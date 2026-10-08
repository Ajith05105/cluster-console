#!/usr/bin/env bash
# Copies the catalog's public images into the cluster's Gitea registry.
#
# The cluster cannot pull from docker.io, so every image the console offers
# must already be in Gitea. This script reads backend/catalog/catalog.json and,
# for each entry that has an "upstream" (a docker.io address), copies the
# arm64 build of that image to the matching Gitea address.
#
# Run it from a machine that can reach both docker.io and Gitea (the laptop),
# AFTER logging in yourself:
#
#     skopeo login --tls-verify=false gitea.cluster.local
#
# The script never sees or stores a password; skopeo uses the login above.
set -euo pipefail

cd "$(dirname "$0")/.."
CATALOG=backend/catalog/catalog.json

# Inside the cluster the registry is called gitea.cluster.local:3000.
# From the laptop the same registry is reached through Traefik, without the
# port. Same registry, same image paths, two front doors.
CLUSTER_NAME="gitea.cluster.local:3000"
LAPTOP_NAME="${REGISTRY:-gitea.cluster.local}"

# One line per catalog entry that has an upstream: "<upstream> <image>".
jq -r '.items[] | select(.upstream != null) | "\(.upstream) \(.image)"' "$CATALOG" |
while read -r upstream image; do
  destination="${image/$CLUSTER_NAME/$LAPTOP_NAME}"

  # Skip images that are already there (for example whoami).
  if skopeo inspect --tls-verify=false --override-os linux --override-arch arm64 \
       "docker://$destination" >/dev/null 2>&1; then
    echo "already in Gitea:  $destination"
    continue
  fi

  echo "copying:           $upstream  ->  $destination"
  skopeo copy --override-os linux --override-arch arm64 --dest-tls-verify=false \
    "docker://$upstream" "docker://$destination"
done

echo "done"
