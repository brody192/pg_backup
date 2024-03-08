FROM golang:1.22.0-alpine as builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . ./

RUN go build -ldflags="-w -s" -o main

FROM alpine:3.18

ARG PG_VERSION='16'

RUN apk add --update --no-cache postgresql${PG_VERSION}-client gzip --repository=https://dl-cdn.alpinelinux.org/alpine/edge/main

WORKDIR /app

COPY --from=builder /app/main ./

CMD pg_isready --dbname=$BACKUP_DATABASE_URL && \
    pg_dump --version && \
    /app/main