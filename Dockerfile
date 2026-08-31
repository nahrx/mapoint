# --- build stage -----------------------------------------------------------
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO_ENABLED=0 for a static binary that runs on the minimal runtime image
# below. The frontend (web/static) is embedded into the binary at build
# time via go:embed, so nothing else needs to be copied into the runtime
# image.
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/se2026-titik-maps .

# --- runtime stage -----------------------------------------------------------
FROM alpine:3.20

# ca-certificates: outbound TLS (tile fetches happen in the browser, not
# here, but this keeps the image ready for anything that does need TLS).
# wget: used by the HEALTHCHECK below.
# tzdata: correct local timestamps in logs.
RUN apk add --no-cache ca-certificates wget tzdata

WORKDIR /app
COPY --from=builder /out/se2026-titik-maps ./se2026-titik-maps

# Runs as an unprivileged user.
RUN adduser -D -H -u 10001 appuser
USER appuser

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["./se2026-titik-maps"]
