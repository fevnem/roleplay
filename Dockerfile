# Multi-stage build: compile in golang, run a static, non-root binary.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Build with CGO off for a fully static binary. -trimpath keeps paths clean.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/roleplay .

# Runtime: tiny alpine, non-root. The app writes a session snapshot to /app/data.
FROM alpine:3.20
RUN adduser -D -H -u 10001 appuser \
    && mkdir -p /app/data \
    && chown -R appuser:appuser /app
WORKDIR /app
COPY --from=build /out/roleplay /app/roleplay
USER appuser
EXPOSE 3000
ENV PORT=3000
ENV SNAPSHOT=/app/data/sessions.json
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:3000/healthz || exit 1
ENTRYPOINT ["/app/roleplay"]