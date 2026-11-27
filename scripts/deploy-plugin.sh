#!/usr/bin/env bash
# Builds the Attendance plugin from this checkout and installs it into the
# Mattermost container with mmctl local mode (no System Console upload, no
# setting changes). Run on the server, from the repo root:
#   scripts/deploy-plugin.sh
# Needs: Docker (BuildKit), Mattermost container "mattermost" with local mode on.
set -euo pipefail
MM=${MM_CONTAINER:-mattermost}
ID=com.bittrix.attendance

rm -rf plugin/dist
DOCKER_BUILDKIT=1 docker build -q -f plugin/Dockerfile --output type=local,dest=plugin/dist plugin >/dev/null
BUNDLE=$(ls plugin/dist/$ID-*.tar.gz)
echo "built $(basename "$BUNDLE")"

scripts/plugin-config.sh
# /tmp in the Mattermost container is a tmpfs, which docker cp can't write
# into; the data directory is a regular mount.
DEST=/mattermost/data/$ID-upload.tar.gz
docker cp "$BUNDLE" "$MM:$DEST"
docker exec "$MM" mmctl --local plugin add --force "$DEST"
docker exec "$MM" mmctl --local plugin enable "$ID"
docker exec "$MM" mmctl --local plugin list | grep -E "$ID|Listing" || true
