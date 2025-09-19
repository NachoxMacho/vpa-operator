#syntax=docker/dockerfile:1.10
# Base Stage
FROM golang:1.24 AS base

WORKDIR /app

# Dependencies
COPY go.mod go.sum ./
RUN go mod download
RUN go mod verify

COPY . ./

FROM golangci/golangci-lint:v2.1.5 AS lint

WORKDIR /app

COPY --from=base /app/ ./
COPY --from=base /go/pkg/mod /go/pkg/mod

RUN golangci-lint run

# Build Stage
FROM base AS build

# Disable CGO so we can run without glibc
RUN CGO_ENABLED=0 GOOS=linux go build -o /docker-go

# Dev build Stage
FROM base AS dev

WORKDIR /app

COPY ./scripts/ /

# This is here to make sure we have a build cache for dev builds in the container.
RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/docker-go

EXPOSE 42069

ENTRYPOINT ["/start.sh", "/app/docker-go"]

FROM gcr.io/distroless/static-debian12:nonroot AS release

WORKDIR /app

COPY --from=build /docker-go /app/docker-go

EXPOSE 42069

ENV ENVIRONMENT=production

# This is needed to run as nonroot
USER nonroot:nonroot

ENTRYPOINT ["/app/docker-go"]
