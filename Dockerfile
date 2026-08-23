# syntax=docker/dockerfile:1

# Assets and binary are built in one Go stage: esbuild is a Go library, so bundling
# the TypeScript needs no Node.js and no npm anywhere in this image.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

WORKDIR /src

# Dependencies first so the layer is reused whenever only sources change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

# Rebuilding the bundle here rather than trusting the committed copy means the image
# can never ship JavaScript that disagrees with the TypeScript sources.
RUN go run ./tools/assets

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/waitformeet ./cmd/waitformeet

# distroless static carries no timezone database. Rather than fetching one, the
# binary imports time/tzdata and embeds it, so the runtime stage needs nothing but
# the binary itself and there is no package download to fail or drift.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/waitformeet /usr/local/bin/waitformeet

# 65532 is distroless' nonroot user. The chart sets fsGroup to match so the
# PersistentVolume is writable.
USER 65532:65532
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080

ENV WFM_LISTEN_ADDR=:8080 \
    WFM_DATA_DIR=/data

ENTRYPOINT ["/usr/local/bin/waitformeet"]
