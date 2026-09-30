FROM golang:1.22-alpine AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o vagustrader ./cmd/vagustrader

FROM alpine:3.19
RUN apk add --no-cache ca-certificates \
    && addgroup -S vagus && adduser -S -G vagus vagus
COPY --from=build /app/vagustrader /usr/local/bin/
COPY config.yml /app/config.yml
COPY config.live.yml /app/config.live.yml
WORKDIR /app
RUN mkdir -p data logs && chown -R vagus:vagus /app
USER vagus
CMD ["vagustrader", "run", "--paper"]
