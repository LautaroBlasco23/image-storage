# Image Storage Service — CLAUDE.md

## Project Overview

A Go service that accepts image uploads via gRPC (protobuf streaming) and serves them over HTTP. Images are stored on disk with auto-generated WebP thumbnails. Metadata is persisted in a SQLite database.

## Architecture

```
cmd/server/main.go       — entrypoint: wires DB, Storage, Handler; starts gRPC + HTTP servers
internal/types.go        — Image struct (domain model)
internal/db.go           — SQLite persistence layer (DB struct)
internal/storage.go      — Filesystem operations + thumbnail generation (Storage struct)
internal/handlers.go     — gRPC service implementation + HTTP file serving (ImageHandler struct)
proto/imagestore/v1/     — Protobuf definitions for ImageService
```

## Servers

Two servers run concurrently:

| Server | Default address | Env override |
|--------|----------------|--------------|
| gRPC   | `127.0.0.1:50051` | `BIND_ADDR` |
| HTTP   | `127.0.0.1:8087`  | `BIND_ADDR` |

- `BIND_ADDR` controls the bind interface for both servers (default `127.0.0.1`).
- `BASE_URL` sets the public base for generated image URLs (default `http://localhost:8087`).
- HTTP timeouts: read/write 15s, idle 60s.
- gRPC reflection is enabled.

## gRPC API (`proto/imagestore/v1/imagestore.proto`)

| RPC | Type | Description |
|-----|------|-------------|
| `UploadImage` | client-streaming | Stream metadata then raw bytes; returns image ID + URLs |
| `GetImageMetadata` | unary | Fetch metadata by image ID |
| `ListImages` | unary | Paginated list by user ID (default page 10, max 100) |
| `DeleteImage` | unary | Delete image + files; validates `user_id` ownership |
| `GetImageURL` | unary | Compute URL/thumbnail URL from image ID |

### Upload stream protocol
1. First message: `UploadImageRequest.metadata` (`ImageMetadataInput`: `user_id`, `filename`, `content_type`)
2. Subsequent messages: `UploadImageRequest.chunk` (raw bytes)
3. Server accumulates all chunks in memory, then saves atomically.

## HTTP API

| Route | Method | Description |
|-------|--------|-------------|
| `GET /images/{imageID}` | GET | Serve original image with its stored `content_type` |
| `GET /images/{imageID}?thumbnail=true` | GET | Serve WebP thumbnail (`image/webp`) |
| `GET /health` | GET | Returns `200 OK` with body `OK` |

- Cache-Control header set to `public, max-age=31536000` (1 year).
- Path traversal is validated before reading any file (`validateImagePath`).

## Storage Logic

### Directory layout on disk

```
./images/
  imagestore.db               ← SQLite database
  originals/{userID}/{uuid}.{ext}         ← original file
  thumbnails/{userID}/{uuid}_thumb.webp   ← thumbnail
```

### SaveImage flow (`internal/storage.go:SaveImage`)
1. Generate a UUID as `imageID`.
2. Read all bytes into memory with `io.ReadAll`.
3. Decode image with `image.Decode` (supports JPEG, PNG).
4. Extract dimensions and validate they fit `int32`.
5. Determine file extension: use the extension from `filename` if safe (≤5 chars, starts with `.`, no path chars), otherwise fall back to the decoded format string.
6. Write original bytes to `./images/originals/{userID}/{imageID}.{ext}` (mode `0600`).
7. Generate thumbnail with `resize.Thumbnail(200, 200, img, Lanczos3)`.
8. Encode thumbnail as lossy WebP (quality 80) and write to `./images/thumbnails/{userID}/{imageID}_thumb.webp`.
9. On any failure after writing the original, the original is cleaned up (best-effort).
10. Return relative paths (`originals/…`, `thumbnails/…`) — **not** absolute paths. These are stored in the DB.

### Path security
- `Storage.validatePath` checks that every file path stays within `baseDir` by comparing the cleaned absolute path prefix. Used on both write and delete.
- `validateImagePath` in `handlers.go` re-validates before serving files over HTTP, resolving symlinks via `filepath.Abs` and checking the relative path does not start with `..`.

### DB (`internal/db.go`)
- SQLite via `go-sqlite3` (CGO required).
- DB file path: `./images/imagestore.db`.
- Auto-migrates on startup (`CREATE TABLE IF NOT EXISTS images …`).
- Index on `user_id` for list/count queries.
- Paths stored in DB are **relative to the images directory** (e.g., `originals/user123/abc.jpg`).
- `GetImagePath` in `Storage` reconstructs full path: `filepath.Join(baseDir, relativePath)`.

## Key Dependencies

| Package | Purpose |
|---------|---------|
| `google.golang.org/grpc` | gRPC server |
| `google.golang.org/protobuf` | Protobuf serialization |
| `github.com/mattn/go-sqlite3` | SQLite driver (CGO) |
| `github.com/google/uuid` | UUID generation for image IDs |
| `github.com/chai2010/webp` | WebP encoding for thumbnails |
| `github.com/nfnt/resize` | Thumbnail resizing (Lanczos3) |

## Development Commands

```bash
make dev            # go run ./cmd/server
make proto          # regenerate protobuf Go code
make install-tools  # install protoc plugins + golangci-lint
make lint           # run golangci-lint
make lint-fix       # run golangci-lint with auto-fix
make prod-build     # docker-compose build
make prod-up        # docker-compose up -d
make prod-down      # docker-compose down
```

## Docker / Production

- Multi-stage Dockerfile: builder (`golang:1.24-alpine`) runs `protoc` + `go build`; final image is `alpine:latest`.
- CGO is enabled (`CGO_ENABLED=1`); `libwebp` and `musl-dev` are required at build time.
- Final image runs as non-root user `appuser` (UID 1001).
- Ports `50051` (gRPC) and `8087` (HTTP) are exposed.
- `docker-compose.yml` mounts `./images` to `/root/images` for persistent storage.

## Important Behaviors & Constraints

- Images are fully buffered in memory during upload before being written to disk. Large images will consume significant memory.
- Thumbnails are always WebP regardless of the original format.
- Thumbnail max dimension is hardcoded at **200px** (`thumbnailSize = 200`).
- `ListImages` pagination uses numeric offset as `page_token` (not a cursor).
- `DeleteImage` enforces ownership: `user_id` in the request must match the stored `user_id`.
- DB deletion and file deletion are not atomic: files are deleted first (best-effort), then the DB record is removed. A failure in file deletion is logged but does not abort the DB deletion.
- On upload rollback (DB save fails), file deletion is also best-effort and only logged on failure.
