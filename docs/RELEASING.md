# Releasing desktop artifacts

This project ships GUI desktop artifacts built with `-tags 'miqt serial'`.

## Artifacts

- `DDGo-macos-arm64.dmg`
- `DDGo-macos-amd64.dmg`
- `DDGo-macos-amd64-macos15.dmg`
- `DDGo-windows-amd64.zip`

Universal macOS artifacts are intentionally **not** published until all Mach-O files in the app bundle are verified universal.

## GitHub Actions runner policy

Use explicit macOS runner labels for release builds:

- Intel / amd64 (Sonoma target build): `macos-14-large`
- Intel / amd64 (host comparison build): `macos-15-intel`
- Apple Silicon / arm64: `macos-14`

Do not use `macos-latest` for release artifacts because it can move to a newer OS or architecture and change the Qt/Homebrew environment under the workflow.

`macos-14-large` is GitHub's x64 macOS 14 runner label and is part of GitHub's macOS larger-runner labels. If the Intel job fails before any steps/logs are emitted, that indicates runner allocation/availability for `macos-14-large` rather than a DDGo build/package failure.

The additional `macos-15-intel` leg produces a separate Intel artifact that targets macOS 15+.

## macOS

macOS artifacts:

- `DDGo-macos-arm64.dmg`
  - Built on `macos-14` arm64
  - Minimum macOS: `14.0`

- `DDGo-macos-amd64.dmg`
  - Built on `macos-14-large` x64, when that runner is available
  - Minimum macOS: `14.0`

- `DDGo-macos-amd64-macos15.dmg`
  - Built on `macos-15-intel` x64
  - Minimum macOS: `15.0`
  - This is not a Sonoma/macOS 14-compatible artifact

The macOS 15 Intel artifact exists to support Intel users on macOS 15+ and to keep testing the Intel build path while macOS 14 Intel runner availability is unresolved. It must not be presented as the fix for Intel Sonoma/macOS 14 users.

When running local release checks, keep runtime targeting explicit per artifact:

1. Build binary (Intel macOS 14 artifact):
   `GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=14.0 go build -tags 'miqt serial' -o dist/ddgo-darwin-amd64 ./cmd/ddgo`
2. Package DMG (Intel macOS 14 artifact):
   `MACOSX_DEPLOYMENT_TARGET=14.0 scripts/package-macos.sh amd64 dist/ddgo-darwin-amd64 dist/DDGo-macos-amd64.dmg v0.0.0`
3. Verify app internals (Intel macOS 14 artifact):
   `scripts/verify-macos-app.sh dist/DDGo.app x86_64 14.0`
4. Build/package/verify the macOS 15 Intel artifact with `MACOSX_DEPLOYMENT_TARGET=15.0` and verifier target `15.0`.

Repeat with `GOARCH=arm64`, `DDGo-macos-arm64.dmg`, and expected arch `arm64` for Apple Silicon (`MACOSX_DEPLOYMENT_TARGET=14.0`, verifier `14.0`).

`package-macos.sh` keeps the deployed app bundle at `dist/DDGo.app`, sets `LSMinimumSystemVersion` in `Info.plist`, and uses `macdeployqt` to bundle Qt frameworks/plugins.

The verifier checks every Mach-O file in the app bundle, not only the main executable. This catches cases where the executable has the right architecture but bundled Qt frameworks/plugins do not.

## Windows (MSYS2 / MinGW)

1. Install packages in an MSYS2 MINGW64 shell:
   `pacman -S --needed mingw-w64-x86_64-go mingw-w64-x86_64-gcc mingw-w64-x86_64-qt5-base mingw-w64-x86_64-pkgconf zip`
2. Build executable in that shell:
   `GOOS=windows GOARCH=amd64 CGO_ENABLED=1 go build -tags 'miqt serial' -ldflags '-s -w -H windowsgui' -o dist/ddgo-windows-amd64.exe ./cmd/ddgo`
3. Package zip with runtime DLLs:
   `scripts/package-windows-msys2.sh dist/ddgo-windows-amd64.exe dist/DDGo-windows-amd64.zip`
4. Verify distribution layout after extraction:
   `pwsh -File scripts/verify-windows-dist.ps1 -DistDir extracted/DDGo`

The zip contains a `DDGo/` folder. Testers should extract the whole zip and run `DDGo/DDGo.exe`; moving the exe out of that folder will break Qt DLL/plugin loading.

The package must include Qt GUI DLLs, MinGW runtime DLLs, and `platforms/qwindows.dll`.

The `macos-15-intel` artifact is a macOS 15+ Intel artifact and must not be presented as the Sonoma/macOS 14 Intel build.
