# Swift SDK tests

Verified 2026-11-04 on macOS (arm64), Xcode 27.2, Swift 6.4.2. Run from the repo root (Package.swift is there, D17).
Tests use Swift Testing and a URLProtocol mock (no network); each test has its own temp queue file,
UserDefaults suite, mock host and clock, so they run in parallel.

    # macOS, the quick loop (36 tests, ~1 s after the first build)
    swift test

    # one suite or test
    swift test --filter DeliveryTests
    swift test --filter "SessionTests/sessionRollsOverAfter30MinutesOfInactivity"

    # iOS simulator (34 tests: the macOS-only gunzip test is excluded)
    xcodebuild test -scheme Sonda -destination 'platform=iOS Simulator,name=iPhone 17,OS=26.2' \
      -derivedDataPath .build/xcode
    # summary only:  ... 2>&1 | grep -E "Test run with|TEST SUCCEEDED|TEST FAILED"

    # compile the production code paths (no DEBUG, no simulator: AppTransaction check)
    xcodebuild build -scheme Sonda -configuration Release -destination 'generic/platform=iOS' -derivedDataPath .build/xcode
    xcodebuild build -scheme Sonda -configuration Release -destination 'generic/platform=macOS' -derivedDataPath .build/xcode
    xcodebuild build -scheme Sonda -configuration Release -destination 'generic/platform=macOS,variant=Mac Catalyst' -derivedDataPath .build/xcode

Other runtimes: `xcrun simctl list devices available`, then change `name=` / `OS=`.
`.build/` (SwiftPM and the xcodebuild derived data above) is in .gitignore.

## End to end against the real server (optional)

`LiveServerTests` is skipped unless `SONDA_E2E_ENDPOINT` and `SONDA_E2E_KEY` are set. With Go via Docker
(notes/recipes/go-via-docker.md) and a scratch directory `$E2E` outside the repo:

    docker run --rm -v "$PWD":/src:ro -w /src -v "$E2E":/out -v sonda-gomod:/go/pkg/mod \
      -v sonda-gocache:/root/.cache/go-build -e CGO_ENABLED=0 golang:1.27-alpine \
      go build -buildvcs=false -o /out/sonda ./cmd/sonda
    docker run --rm -v "$E2E":/data alpine:3 /data/sonda app create e2e      # prints sk_...
    docker run -d --rm --name sonda-sdk-e2e -p 127.0.0.1:18080:8080 -v "$E2E":/data alpine:3 /data/sonda serve
    SONDA_E2E_ENDPOINT=http://127.0.0.1:18080 SONDA_E2E_KEY=sk_... swift test --filter LiveServerTests
    docker logs sonda-sdk-e2e      # expect accepted=100, then accepted=100 duplicates=100, then accepted=23
    docker stop sonda-sdk-e2e

`-buildvcs=false` because the worktree's `.git` is a file pointing outside the mount.
Rows can be inspected with `sqlite3 -readonly "$E2E/sonda.db" "select * from events limit 3"`.
