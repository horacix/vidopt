# vidopt: NAS & Workstation Video Storage Optimizer

<p align="center">
  <a href="https://github.com/horacix/vidopt/actions/workflows/ci.yml"><img src="https://github.com/horacix/vidopt/actions/workflows/ci.yml/badge.svg" alt="CI Status"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.23-blue.svg" alt="Go Version"></a>
  <a href="Dockerfile"><img src="https://img.shields.io/badge/Docker-Ready-2496ED.svg?logo=docker&logoColor=white" alt="Docker Ready"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-green.svg" alt="License: MIT"></a>
  <a href="CODE_OF_CONDUCT.md"><img src="https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa.svg" alt="Code of Conduct"></a>
</p>

<p align="center">
  <strong>vidopt</strong> is a lightweight, strongly typed storage optimizer for media collections (Plex, Jellyfin, Emby, NAS archives). It uses <strong>adaptive pre-flight sample probing (inspired by <a href="https://github.com/alexheretic/ab-av1">ab-av1</a>)</strong> to accurately verify that video re-encoding meets your space savings threshold (e.g. &ge;20%) <em>before</em> committing GPU or CPU time to full transcodes.
</p>

---

## How It Works

1. **Pre-Flight Sampling**: Automatically extracts short 5–10s clips across the timeline to forecast exact space savings. If projected savings are below your threshold (default: 20%), the file is safely skipped.
2. **RAID-Safe Scratch Staging**: All temporary clips and encoding operations happen strictly inside a local scratch directory (`scratch_path`) to prevent parity drive thrashing and backup churn.
3. **Atomic Replacement & Integrity**: Verifies output duration and size before atomically swapping the file (or outputting to a separate folder), preserving original modification timestamps (`mtime`) and all audio/subtitle tracks losslessly.

---

## Why `vidopt`?

Existing library re-encoders either require heavy distributed stacks, lack pre-flight estimation, or are single-file manual CLI scripts:

| Feature | `vidopt` | Tdarr | Unmanic | ab-av1 |
| :--- | :---: | :---: | :---: | :---: |
| **Footprint / Memory** | **~20–30 MB** (Single static Go binary) | 500 MB – 1.5 GB (Node/Mongo) | ~150–300 MB (Python) | Minimal (Rust CLI) |
| **Pre-Flight Sample Testing** | **Adaptive 5s–10s clips (auto)** | Custom plugins required | None (transcodes full file first) | Manual per-file CLI |
| **Target Hardware** | **NAS (Intel QSV/VAAPI) & Workstation (NVIDIA NVENC)** | Distributed nodes | Single server / nodes | Local workstation |
| **RAID / Parity Protection** | **Strict scratch staging + atomic swap** | Cache directory | Cache directory | In-place or specified path |
| **Operational Modes** | **In-Place OR Separate Output Folder** | In-place / output folder | Output folder | Output file |
| **Metadata & Subtitles** | **Lossless stream-copy (`-c:a copy -c:s copy`)** | Plugin-dependent | Plugin-dependent | Copy or encode |
| **Timestamp Preservation** | **Native (`mtime` preserved for Plex/Jellyfin)** | Plugin-dependent | Plugin-dependent | Manual |
| **Zero-CGO SQLite** | **Native pure Go (WAL mode)** | MongoDB / SQLite | SQLite | None (stateless) |

---

## Hardware Acceleration Matrix

`vidopt` dynamically inspects available hardware and chooses the optimal encoder:

| Hardware Environment | Target Codec | Selected Encoder | Hardware Acceleration |
| :--- | :--- | :--- | :---: |
| **Desktop Workstation (RTX 40-series)** | AV1 | `av1_nvenc` | NVIDIA CUDA / NVENC :rocket: |
| **Desktop Workstation (RTX 20/30-series)** | HEVC / H.265 | `hevc_nvenc` | NVIDIA CUDA / NVENC :rocket: |
| **Intel Arc / Core 14th+ Gen** | AV1 | `av1_qsv` | Intel Quick Sync :rocket: |
| **Intel N-series (N100, N95, N200, N305)** | HEVC / H.265 | `hevc_qsv` | Intel Quick Sync :rocket: |
| **Synology / Linux NAS (VAAPI)** | AV1 / HEVC | `av1_vaapi` / `hevc_vaapi` | Linux VAAPI :rocket: |
| **CPU Fallback (Any Platform)** | AV1 | `libsvtav1` | Multi-threaded Software (SVT-AV1) |
| **CPU Fallback (Any Platform)** | HEVC | `libx265` | Multi-threaded Software (x265) |

---

## Deployment & Usage

### 1. Workstation Mode (Native `.exe` / Linux CLI)

Run `vidopt` directly on your desktop workstation (equipped with an NVIDIA RTX or Intel Arc GPU) to process video files stored on local drives or NAS network shares (SMB / NFS).

> [!IMPORTANT]
> **Prerequisite for Native CLI (`.exe`)**:
> While the Docker container bundles FFmpeg automatically, the standalone native binary delegates encoding to the system's `ffmpeg` and `ffprobe`. If `vidopt hwcheck` reports no available encoders, install FFmpeg on your system:
> - **Windows (PowerShell)**:
>   ```powershell
>   winget install Gyan.FFmpeg
>   ```
>   *(After installing, reopen PowerShell so `ffmpeg` is available in your PATH).*
> - **macOS**: `brew install ffmpeg`
> - **Linux (Ubuntu/Debian)**: `sudo apt install ffmpeg`

#### Common CLI Commands:
```powershell
# 1. Check detected GPU acceleration and encoders
.\vidopt.exe hwcheck

# 2. Inspect a video and run an adaptive sample probe (forecast savings without transcoding)
.\vidopt.exe probe "Z:\Movies\SampleMovie.mkv"

# 3. Transcode a single video directly
.\vidopt.exe process "Z:\Movies\SampleMovie.mkv"

# 4. Run a full library scan (transcodes qualifying files saving >= 20%)
.\vidopt.exe scan --library-path "Z:\Movies" --min-savings 20

# 5. Display cumulative space saved and file statistics
.\vidopt.exe stats
```

---

### 2. NAS Container Mode (Docker & Synology / TrueNAS)

The container image includes FFmpeg, Intel VAAPI drivers, and Quick Sync support out-of-the-box.

#### `docker-compose.yml`
```yaml
services:
  vidopt:
    image: vidopt:latest
    build: .
    container_name: vidopt
    restart: unless-stopped
    devices:
      - /dev/dri:/dev/dri # Pass through Intel Quick Sync / VAAPI render node
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=America/New_York
      - VIDOPT_LIBRARY_PATH=/library
      # Optional separate output destination (Unmanic-style):
      # - VIDOPT_OUTPUT_PATH=/output
      - VIDOPT_SCRATCH_PATH=/scratch
      - VIDOPT_DATABASE_PATH=/data/vidopt.db
      - VIDOPT_TARGET_CODEC=auto        # auto, av1, or hevc
      - VIDOPT_MIN_SAVINGS_PERCENT=20   # minimum percentage savings required
      - VIDOPT_SCAN_SCHEDULE=@daily     # @daily, @hourly, or cron syntax
      - VIDOPT_WATCH_REALTIME=false     # true to enable fsnotify watcher
      - VIDOPT_PRESERVE_TIMESTAMPS=true # preserve mtime for media server indexing
      - VIDOPT_KEEP_BACKUP=true         # keep .original file in in-place mode
    volumes:
      - /volume1/video:/library
      - /volume1/scratch:/scratch       # fast SSD scratch path
      - ./data:/data                    # stores vidopt.db and optional config.yaml
```

Launch the daemon:
```bash
docker compose up -d
```

---

### 3. Using `config.yaml` with Docker Compose

You can manage all settings using a `config.yaml` file instead of cluttering your `docker-compose.yml` with environment variables.

#### How It Works:
`vidopt` automatically checks `/data/config.yaml` inside the container. Since the compose file mounts `./data:/data`, placing `config.yaml` in your local `./data` folder makes it available immediately!

Furthermore, **3-tier precedence** applies:
`CLI Flags > Environment Variables (VIDOPT_*) > config.yaml > Built-in Defaults`.
Any variable defined in `docker-compose.yml` overrides the corresponding value in `config.yaml`.

#### Step-by-Step Setup:
1. Generate a commented configuration template:
   ```bash
   # Linux/Docker:
   docker run --rm -v ${PWD}/data:/data vidopt config init
   # Or using Windows .exe:
   .\vidopt.exe config init
   ```
2. Move `config.yaml` into your `./data` directory (`./data/config.yaml`):
   ```yaml
   # ./data/config.yaml
   library_path: "/library"
   output_path: ""                 # empty = in-place replacement
   scratch_path: "/scratch"
   database_path: "/data/vidopt.db"
   target_codec: "auto"            # auto, av1, or hevc
   min_savings_percent: 20.0
   target_crf: 28
   encoder_preset: "6"
   scan_schedule: "@daily"
   watch_realtime: false
   debounce_seconds: 60
   preserve_timestamps: true
   keep_backup: true
   ```
3. Simplify your `docker-compose.yml` down to basic mounts:
   ```yaml
   services:
     vidopt:
       image: vidopt:latest
       container_name: vidopt
       restart: unless-stopped
       devices:
         - /dev/dri:/dev/dri
       environment:
         - PUID=1000
         - PGID=1000
         - TZ=America/New_York
       volumes:
         - /volume1/video:/library
         - /volume1/scratch:/scratch
         - ./data:/data  # Contains config.yaml and vidopt.db
   ```
4. Start the service:
   ```bash
   docker compose up -d
   ```

---

## Configuration Reference

| Setting | Environment Variable | CLI Flag | Default | Description |
| :--- | :--- | :--- | :--- | :--- |
| `library_path` | `VIDOPT_LIBRARY_PATH` | `--library-path` | `./library` | Source media folder |
| `output_path` | `VIDOPT_OUTPUT_PATH` | `--output-path` | `""` | Separate destination (empty = in-place) |
| `scratch_path` | `VIDOPT_SCRATCH_PATH` | `--scratch-path` | `./scratch` | Staging directory for temp encodes |
| `database_path` | `VIDOPT_DATABASE_PATH` | `--database-path` | `./data/vidopt.db`| SQLite tracking database path |
| `min_savings_percent`| `VIDOPT_MIN_SAVINGS_PERCENT` | `--min-savings`| `20.0` | Minimum % savings required |
| `target_codec` | `VIDOPT_TARGET_CODEC` | `--target-codec` | `auto` | `auto`, `av1`, or `hevc` |
| `target_crf` | `VIDOPT_TARGET_CRF` | `--crf` | `0` (auto) | `0` = perceptual quality CRF search; `>0` = fixed CRF |
| `target_vmaf` | `VIDOPT_TARGET_VMAF` | `--vmaf` | `93.0` | Target VMAF perceptual quality score (`93.0` = perceptually lossless; `0` disables) |
| `crf_tiers` | (YAML only) | — | `sd:22, hd:24, fhd:26, uhd:28` | Initial seed CRF tiers for dynamic search (interlaced/DV auto-receives -2) |
| `encoder_preset` | `VIDOPT_ENCODER_PRESET` | `--preset` | `6` | Speed preset (e.g. `6` for SVT-AV1, `p6` for NVENC) |
| `hardware_accel` | `VIDOPT_HARDWARE_ACCEL` | `--hw-accel` | `auto` | `auto`, `nvenc`, `qsv`, `vaapi`, `cpu` |
| `deinterlace` | `VIDOPT_DEINTERLACE` | `--deinterlace` | `auto` | `auto` (detects interlaced video & applies `bwdif`), `on`, `off`, or custom filter |
| `reprocess` | `VIDOPT_REPROCESS` | `-r, --reprocess` | `false` | Re-evaluate and re-transcode files even if already completed or in target format (aliases: `-f, --force`) |
| `scan_schedule` | `VIDOPT_SCAN_SCHEDULE` | `--schedule` | `@daily` | Cron expression (`@daily`, `@hourly`, `0 2 * * *`) |
| `watch_realtime` | `VIDOPT_WATCH_REALTIME` | `--watch` | `false` | Enable real-time `fsnotify` file watcher |
| `debounce_seconds` | `VIDOPT_DEBOUNCE_SECONDS` | `--debounce` | `60` | Settling time for in-progress writes |
| `preserve_timestamps`| `VIDOPT_PRESERVE_TIMESTAMPS`| `--preserve-timestamps`| `true` | Preserves modification time (`mtime`) |
| `keep_backup` | `VIDOPT_KEEP_BACKUP` | `--keep-backup` | `true` | Preserves `.original` file in in-place mode |
| `max_workers` | `VIDOPT_MAX_WORKERS` | `--workers` | `1` | Concurrent transcoding jobs |

---

## Contributing & Community

Contributions are warmly welcomed! Please read:
- [CONTRIBUTING.md](CONTRIBUTING.md) — Coding conventions, local testing, and PR checklist.
- [AGENTS.md](AGENTS.md) — Specific guidelines for developers and autonomous AI coding agents.
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) — Community pledge and standards.
- [SECURITY.md](SECURITY.md) — Vulnerability reporting policy.

---

## License

`vidopt` is open-source software licensed under the [MIT License](LICENSE).
