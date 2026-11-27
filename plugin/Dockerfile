# Builds the plugin bundle where Go and Node aren't installed (e.g. the VPS):
#   docker build -f plugin/Dockerfile --output type=local,dest=plugin/dist plugin
# leaves plugin/dist/com.bittrix.attendance-<version>.tar.gz.
FROM golang:1.27-bookworm AS go

FROM node:24-bookworm AS build
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH CGO_ENABLED=0
WORKDIR /src
# Dependencies first, so code changes don't re-download them.
COPY webapp/package.json webapp/package-lock.json webapp/
RUN cd webapp && npm ci --no-audit --no-fund
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The copied node_modules is fresh: stop make from reinstalling it.
RUN touch webapp/node_modules && make dist

FROM scratch
COPY --from=build /src/dist/*.tar.gz /
