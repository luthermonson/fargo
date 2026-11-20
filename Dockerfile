# Build a static rewind binary and ship it on distroless static.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X github.com/luthermonson/rewind/pkg/version.Version=${VERSION}" \
    -o /out/rewind ./cmd/rewind

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/rewind /usr/local/bin/rewind
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/rewind"]
