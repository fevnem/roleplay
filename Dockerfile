# Multi-stage build: compile in golang, run the static binary.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -o /out/roleplay .

# Runtime: tiny alpine with the binary.
FROM alpine:3.20
WORKDIR /app
COPY --from=build /out/roleplay /app/roleplay
RUN mkdir -p /app/data
EXPOSE 3000
ENV PORT=3000
ENV SNAPSHOT=/app/data/sessions.json
ENTRYPOINT ["/app/roleplay"]