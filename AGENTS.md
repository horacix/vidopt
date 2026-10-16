# AGENTS.md - Developer & AI Agent Guidelines for `vidopt`

This document provides architectural guidance, design invariants, coding conventions, and testing procedures for developers and automated AI agents contributing to `vidopt`.

---

## 1. Project Mission & Invariants

`vidopt` is a lightweight, strongly typed NAS video library storage optimizer written in Go. It operates either as a continuous/scheduled background daemon or as a standalone CLI utility.

### Non-Negotiable Invariants:
1. **RAID & Parity Safety**: All intermediate work (test clips, full transcode output) **MUST** be written to `scratch_path`. Never write temporary encoding files directly into the source media library.
2. **Zero-CGO SQLite**: The SQLite database engine must remain pure Go (`modernc.org/sqlite`) so binaries can cross-compile to any platform (`linux/amd64`, `linux/arm64`, `windows/amd64`) without C compiler toolchains.
3. **Adaptive Pre-Flight Sampling**: Never begin a multi-hour transcode blindly. Always sample short representative clips first to verify projected space savings against `min_savings_percent` (default: 20%).
4. **Lossless Audio & Metadata**: Audio and subtitles must never be re-encoded unless explicitly configured. Stream-copying (`-c:a copy -c:s copy -map_metadata 0`) is mandatory to preserve multi-language tracks, surround sound, and subtitles intact.
5. **3-Tier Configuration Precedence**: Settings must adhere to:
   `CLI Flags > Environment Variables (VIDOPT_*) > config.yaml > Defaults`.

---

## 2. Codebase Structure

```
vidopt/
├── cmd/
│   └── vidopt/
│       └── main.go          # Cobra CLI commands & signal handling
├── internal/
│   ├── config/              # Viper configuration parsing & validation
│   ├── database/            # Pure-Go SQLite repository & models (WAL mode)
│   ├── detector/            # Hardware acceleration & ffprobe inspection
│   ├── encoder/             # Adaptive sampling (ab-av1 style) & transcoding
│   ├── monitor/             # fsnotify real-time watcher & cron scheduler
│   └── pipeline/            # Queue worker & lifecycle orchestrator
├── config.example.yaml      # Sample configuration file
├── Dockerfile               # Multi-stage Debian bookworm runtime with VAAPI
├── docker-compose.yml       # Production docker-compose for NAS / Synology
└── go.mod / go.sum
```

---

## 3. Package Responsibilities

- **`internal/config`**: Uses Viper. All environment variables have prefix `VIDOPT_`. Defaults are defined in `DefaultConfig()`.
- **`internal/database`**: Tables: `processed_files`. Always use parameterized SQL queries. Ensure SQLite WAL mode is enabled on connection open.
- **`internal/detector`**:
  - `hardware.go`: Queries FFmpeg hardware accelerators and encoders. Detects Linux `/dev/dri` render node and NVIDIA GPU (`cuda`/`nvidia-smi`).
  - `probe.go`: Executes `ffprobe` in JSON mode. Separates JSON parsing into `ParseProbeJSON` for unit testing with mock JSON.
- **`internal/encoder`**:
  - `sampler.go`: Calculates adaptive sample windows based on duration:
    - $\le 30\text{s}$: Single sample of entire clip.
    - $30\text{s} - 90\text{s}$: 2 samples of 5s each (at 25% and 75%).
    - $> 90\text{s}$: 3 samples of 10s each (at 20%, 50%, and 80%).
  - `transcode.go`: Full encoding pipeline, atomic file move/swap, `.original` backup preservation, and timestamp (`mtime`) preservation.
- **`internal/monitor`**:
  - `watcher.go`: Recursive `fsnotify` watcher with write-debounce settling timer to avoid reading files during active torrent/SMB transfers.
  - `scheduler.go`: Cron scheduler (`@daily`, `@hourly`, or standard 5-part cron syntax).
- **`internal/pipeline`**:
  - `orchestrator.go`: Worker pool and synchronization.

---

## 4. Hardware Acceleration Priority

In `"auto"` mode (`target_codec: "auto"`), optimal encoders are selected in this order:
1. **AV1**:
   - `av1_nvenc` (if NVIDIA GPU detected)
   - `av1_qsv` (if Intel GPU `/dev/dri` detected)
   - `av1_vaapi` (if VAAPI `/dev/dri` detected)
   - `libsvtav1` (CPU software fallback)
2. **HEVC**:
   - `hevc_nvenc` (if NVIDIA GPU detected)
   - `hevc_qsv` (if Intel GPU `/dev/dri` detected, ideal for Intel Alder Lake-N N100/N95)
   - `hevc_vaapi`
   - `libx265` (CPU software fallback)

---

## 5. Development & Testing Workflow

### Running Unit Tests
All packages have comprehensive unit tests without requiring external media files or FFmpeg installed locally:
```bash
go test -v ./...
```
Or via Docker:
```bash
docker run --rm -v ${PWD}:/app -w /app golang:1.23-bookworm go test -v ./...
```

### Compiling Binaries
Cross-compile a standalone static binary for Linux:
```bash
CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o vidopt ./cmd/vidopt
```
Cross-compile for Windows:
```bash
CGO_ENABLED=0 GOOS=windows go build -trimpath -ldflags="-s -w" -o vidopt.exe ./cmd/vidopt
```

### Building the Docker Container
```bash
docker compose build
```

---

## 6. Pull Request & Commit Guidelines

- **Format**: All Go code must be formatted using `gofmt -s -w .`.
- **Linting**: Ensure `golangci-lint run` passes without warnings.
- **Tests**: Add unit test coverage for any new features or bug fixes.
- **Documentation**: If adding new configuration flags or settings, update `config.example.yaml`, `README.md`, and `main.go`.
