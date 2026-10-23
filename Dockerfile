FROM golang:1.27.3-alpine3.24 AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . ./

RUN go build -ldflags="-w -s" -o main ./cmd/pg_backup

FROM alpine:3.24.3

ARG PG_VERSION='18'

RUN apk add --update --no-cache postgresql${PG_VERSION}-client gzip --repository=https://dl-cdn.alpinelinux.org/alpine/edge/main

WORKDIR /app

COPY --from=builder /app/main ./

CMD pg_isready --dbname=$BACKUP_DATABASE_URL && \
    pg_dump --version && \
    exec /app/main
