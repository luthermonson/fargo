# Build a static fargo binary and ship it on distroless static.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
# TARGETOS/TARGETARCH are set by BuildKit from --platform (or the host). They
# must not default to amd64: an amd64 binary on an arm64 node runs emulated and
# segfaults in client-go under load.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-$(go env GOOS)} GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags "-s -w -X github.com/luthermonson/fargo/pkg/version.Version=${VERSION}" \
    -o /out/fargo ./cmd/fargo

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/fargo /usr/local/bin/fargo
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/fargo"]
