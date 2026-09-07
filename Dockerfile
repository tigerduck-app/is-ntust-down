# ─── build ───────────────────────────────────────────────────
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies are cached separately from sources so an edit to a .go file
# does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/isntustup ./cmd/isntustup

# ─── runtime ─────────────────────────────────────────────────
FROM alpine:3.21

# ca-certificates for HTTPS to NTUST; tzdata because days are bucketed in
# Asia/Taipei and the scratch image has no zone database.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 app

COPY --from=build /out/isntustup /usr/local/bin/isntustup

USER app
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/isntustup"]
