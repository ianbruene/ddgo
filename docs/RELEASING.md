# Releasing desktop artifacts

This project ships GUI desktop artifacts built with `-tags 'miqt serial'`.

## Artifacts

- `DDGo-macos-arm64.dmg`
- `DDGo-macos-amd64.dmg`
- `DDGo-windows-amd64.zip`

Universal macOS artifacts are intentionally **not** published until all Mach-O files in the app bundle are verified universal.

## macOS

Use explicit Sonoma runners (`macos-14` for Intel and `macos-14-arm64` for Apple Silicon).

1. Build binary:
   `GOOS=darwin GOARCH=<amd64|arm64> CGO_ENABLED=1 go build -tags 'miqt serial' -o dist/ddgo-darwin-<arch> ./cmd/ddgo`
2. Package DMG:
   `scripts/package-macos.sh <arch> dist/ddgo-darwin-<arch> dist/DDGo-macos-<arch>.dmg <version>`
3. Verify app internals before publish:
   `scripts/verify-macos-app.sh dist/DDGo.app <x86_64|arm64>`

`package-macos.sh` sets `LSMinimumSystemVersion` in `Info.plist` and uses `macdeployqt` to bundle Qt frameworks/plugins.

## Windows (MSYS2 / MinGW)

1. Build executable in MSYS2 shell:
   `GOOS=windows GOARCH=amd64 CGO_ENABLED=1 go build -tags 'miqt serial' -o dist/ddgo-windows-amd64.exe ./cmd/ddgo`
2. Package zip with runtime DLLs:
   `scripts/package-windows-msys2.sh dist/ddgo-windows-amd64.exe dist/DDGo-windows-amd64.zip`
3. Verify distribution layout:
   `pwsh -File scripts/verify-windows-dist.ps1 -DistDir <unzipped-dir>`

The package must include Qt GUI DLLs and `platforms/qwindows.dll`.
