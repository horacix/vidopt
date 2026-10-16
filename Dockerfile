# syntax=docker/dockerfile:1

# ---------------------------------------------------------
# Build Stage
# ---------------------------------------------------------
FROM golang:1.27-bookworm AS builder

WORKDIR /build

# Copy source files
COPY . .

# Download dependencies and generate go.sum
RUN go mod tidy && go mod download

# Compile static vidopt binary without CGO (pure Go SQLite)
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /build/vidopt ./cmd/vidopt

# ---------------------------------------------------------
# Production Runtime Stage
# ---------------------------------------------------------
FROM debian:bookworm-slim

LABEL maintainer="horacix"
LABEL description="NAS Video Storage Optimizer with Intel QSV, VAAPI, and SVT-AV1"

# Install FFmpeg and Intel GPU VAAPI / QSV driver packages
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    intel-media-va-driver \
    va-driver-all \
    vainfo \
    ca-certificates \
    tzdata \
    gosu \
    && rm -rf /var/lib/apt/lists/*

# Default container directories
RUN mkdir -p /library /output /scratch /data

# Copy compiled binary from builder
COPY --from=builder /build/vidopt /usr/local/bin/vidopt

# Default environment variables
ENV VIDOPT_LIBRARY_PATH=/library \
    VIDOPT_OUTPUT_PATH="" \
    VIDOPT_SCRATCH_PATH=/scratch \
    VIDOPT_DATABASE_PATH=/data/vidopt.db \
    VIDOPT_TARGET_CODEC=auto \
    VIDOPT_MIN_SAVINGS_PERCENT=20 \
    VIDOPT_SCAN_SCHEDULE="@daily" \
    VIDOPT_WATCH_REALTIME=false \
    VIDOPT_PRESERVE_TIMESTAMPS=true \
    VIDOPT_KEEP_BACKUP=true \
    PUID=1000 \
    PGID=1000

# Entrypoint script handling PUID/PGID permissions for NAS volume mounts
RUN printf '#!/bin/sh\n\
PUID="${PUID:-1000}"\n\
PGID="${PGID:-1000}"\n\
case "$PUID" in *[!0-9]*|"") PUID=1000 ;; esac\n\
case "$PGID" in *[!0-9]*|"") PGID=1000 ;; esac\n\
if [ "$PUID" -ne 0 ]; then\n\
    groupadd -g "$PGID" vidopt 2>/dev/null || true\n\
    useradd -u "$PUID" -g "$PGID" -d /data -s /bin/sh vidopt 2>/dev/null || true\n\
    chown -R "$PUID:$PGID" /data /scratch 2>/dev/null || true\n\
    exec gosu "$PUID:$PGID" vidopt "$@"\n\
fi\n\
exec vidopt "$@"\n' > /entrypoint.sh && chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
CMD ["run"]
