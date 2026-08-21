# syntax=docker/dockerfile:1
# Build stage runs on the build host's platform and cross-compiles for the
# target; the final image is a static binary on scratch, so the lab never
# pulls a base layer for it.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=$VERSION" -o /out/svc .

FROM scratch
COPY --from=build /out/svc /svc
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/svc"]
