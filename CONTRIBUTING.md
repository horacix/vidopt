# Contributing to `vidopt`

Thank you for your interest in improving `vidopt`! We welcome bug reports, documentation enhancements, feature requests, and code contributions.

---

## 1. Ground Rules & Design Principles

Before submitting code, please keep in mind:
- **No CGO**: We strictly use pure-Go SQLite (`modernc.org/sqlite`) so binaries can easily cross-compile without C compiler toolchains.
- **Scratch Drive Isolation**: Never output partial encoding files directly to user library paths. All temporary encoding happens in `scratch_path`.
- **Pre-Flight Sampling**: Any new transcoding feature must respect the sample evaluation phase so users never blindly transcode files that won't yield space savings.
- **Lossless Audio/Subs**: Keep audio, subtitle, and metadata streams intact unless explicitly requested by the user.

---

## 2. Setting Up Your Environment

### Prerequisites:
- Go 1.23+ (or Docker)
- FFmpeg 5.0+ and `ffprobe` (optional if using Docker)

### Running Tests:
```bash
go test -v ./...
```
Or with coverage:
```bash
go test -v -race -coverprofile=coverage.out ./...
```

### Running with Docker:
```bash
docker run --rm -v ${PWD}:/app -w /app golang:1.23-bookworm go test -v ./...
```

---

## 3. Pull Request Process

1. Fork the repository and create your feature branch:
   ```bash
   git checkout -b feature/my-new-feature
   ```
2. Format your code with `gofmt`:
   ```bash
   gofmt -s -w .
   ```
3. Ensure all tests pass. If you're adding new functionality, write matching unit tests in `*_test.go`.
4. Commit your changes following conventional commits (`feat: ...`, `fix: ...`, `docs: ...`, `chore: ...`).
5. Open a Pull Request against the `main` branch. Provide a clear description of the problem and your solution.

---

## 4. Code of Conduct

All contributors are expected to uphold our [Code of Conduct](CODE_OF_CONDUCT.md).
