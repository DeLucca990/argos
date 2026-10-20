FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /argos ./cmd/argos && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
ENV TZ=America/Sao_Paulo
COPY --from=build /argos /argos
COPY --from=build --chown=65532:65532 /data /data
VOLUME /data
HEALTHCHECK --interval=1m --timeout=10s --start-period=1m CMD ["/argos", "healthcheck"]
ENTRYPOINT ["/argos"]
