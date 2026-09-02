FROM golang:1.27 AS builder
WORKDIR /app
COPY go-arena/go.mod go-arena/go.sum ./go-arena/
RUN cd go-arena && go mod download
COPY go-arena/ ./go-arena/
# Build identity: pass GIT_COMMIT / BUILD_TIME so the live server can report
# which commit it is running (GET /api/v1/version, frontend About dialog).
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown
RUN cd go-arena && CGO_ENABLED=0 go build \
    -ldflags "-X arena-server/internal/version.Commit=${GIT_COMMIT} -X arena-server/internal/version.BuildTime=${BUILD_TIME}" \
    -o /arena-server ./cmd/arena-server

FROM gcr.io/distroless/static:nonroot
COPY --from=builder --chown=nonroot:nonroot /arena-server /arena-server
COPY --chown=nonroot:nonroot frontend/ /frontend/
USER nonroot:nonroot
EXPOSE 8000
CMD ["/arena-server"]
