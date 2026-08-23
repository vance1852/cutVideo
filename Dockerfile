FROM golang:1.22 AS build

ENV GOTOOLCHAIN=local
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations

RUN CGO_ENABLED=0 go build -trimpath -o /out/cutvideo ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/cutvideo /app/cutvideo

ENV CUTVIDEO_HTTP_ADDR=":8080" \
    CUTVIDEO_DATABASE_DSN="file:/app/data/cutvideo.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" \
    CUTVIDEO_LOG_FORMAT="json"

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/cutvideo"]
