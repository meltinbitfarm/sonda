# Go via Docker (Go is not installed on the host)

Verified 2026-11-04 on macOS (arm64), Docker 29.8, image `golang:1.27-alpine` (go1.27.3).
Run from the repo root. Two named volumes keep the module cache and build cache between runs
(first run downloads modules, later runs are fast). `CGO_ENABLED=0` because modernc.org/sqlite is pure Go
and the binary must be static (D2, D3).

Common prefix (copy as is):

    GO='docker run --rm -v "$PWD":/src -w /src -v sonda-gomod:/go/pkg/mod -v sonda-gocache:/root/.cache/go-build -e CGO_ENABLED=0 golang:1.27-alpine'

Commands:

    # vet + tests (what to run before every commit)
    eval $GO sh -c "'go vet ./... && go test ./...'"

    # one package, verbose
    eval $GO go test -v ./internal/store/

    # format check (prints files needing gofmt; empty output = clean)
    eval $GO gofmt -l .

    # tidy after adding/removing imports
    eval $GO go mod tidy

    # add a dependency
    eval $GO go get example.com/mod@latest

    # build a static linux binary (arch = host's; add -e GOARCH=amd64 for x86 servers)
    eval $GO go build -trimpath -ldflags="-s -w" -o bin/sonda ./cmd/sonda

Smoke test of the CLI and server inside one container:

    docker run --rm -v "$PWD":/src -w /src -v sonda-gomod:/go/pkg/mod -v sonda-gocache:/root/.cache/go-build \
      -e CGO_ENABLED=0 golang:1.27-alpine sh -c '
        go build -o /tmp/sonda ./cmd/sonda && export SONDA_DB_PATH=/tmp/t.db &&
        /tmp/sonda app create "Demo" && /tmp/sonda app list &&
        (SONDA_ADDR=127.0.0.1:8080 /tmp/sonda serve & sleep 1; wget -qO- http://127.0.0.1:8080/healthz; kill %1; wait)'

Notes:
- `go test -race` needs CGO and is not available with this recipe (alpine has no gcc); use
  `golang:1.27` (Debian) with `-e CGO_ENABLED=1` if a race run is needed.
- Files created in the bind mount (go.sum, binaries) end up owned by your user on Docker Desktop for Mac.
- When go.mod's `go` line is bumped, bump the image tag here too.
