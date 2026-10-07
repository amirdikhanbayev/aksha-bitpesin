
FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/bot /bot

ENV DEFAULT_TIMEZONE=Asia/Almaty \
    DEFAULT_CURRENCY=KZT \
    SCHEDULER_INTERVAL=15m \
    FETCH_RATES=true \
    HTTP_ADDR=:8080 \
    DEBUG=false

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/bot", "-health"]

USER nonroot:nonroot
ENTRYPOINT ["/bot"]
