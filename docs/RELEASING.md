# Releasing desktop artifacts

This project ships GUI desktop artifacts built with `-tags 'miqt serial'`.

## Artifacts

- `DDGo-macos-arm64.dmg`
- `DDGo-macos-amd64.dmg`
- `DDGo-windows-amd64.zip`

Universal macOS artifacts are intentionally **not** published until all Mach-O files in the app bundle are verified universal.

## GitHub Actions runner policy

Use explicit macOS runner labels for release builds:

- Intel / amd64: `macos-14-large`
- Apple Silicon / arm64: `macos-14`

Do not use `macos-latest` for release artifacts because it can move to a newer OS or architecture and change the Qt/Homebrew environment under the workflow.

`macos-14-large` is GitHub's x64 macOS 14 runner label and is part of GitHub's macOS larger-runner labels. If the Intel job fails before any steps/logs are emitted, that indicates runner allocation/availability for `macos-14-large` rather than a DDGo build/package failure.

## macOS

Keep macOS runtime targeting pinned to Sonoma unless intentionally dropping support:

- Intel macOS CI runner: `macos-14-large`
- Intel runtime target: macOS `14.0`
- Verifier target: `14.0`
- Do not bump `LSMinimumSystemVersion`, `MACOSX_DEPLOYMENT_TARGET`, or verifier max-minos to 15 unless intentionally dropping Sonoma support.

1. Build binary:
   `GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=14.0 go build -tags 'miqt serial' -o dist/ddgo-darwin-amd64 ./cmd/ddgo`
2. Package DMG:
   `scripts/package-macos.sh amd64 dist/ddgo-darwin-amd64 dist/DDGo-macos-amd64.dmg v0.0.0`
3. Verify app internals before publish:
   `scripts/verify-macos-app.sh dist/DDGo.app x86_64 14.0`

Repeat with `GOARCH=arm64`, `DDGo-macos-arm64.dmg`, and expected arch `arm64` for Apple Silicon.

`package-macos.sh` keeps the deployed app bundle at `dist/DDGo.app`, sets `LSMinimumSystemVersion` in `Info.plist`, and uses `macdeployqt` to bundle Qt frameworks/plugins.

The verifier checks every Mach-O file in the app bundle, not only the main executable. This catches cases where the executable has the right architecture but bundled Qt frameworks/plugins do not.

If Intel verification fails (for example, a bundled Qt framework/plugin reports deployment target above 14.0), keep the Intel job failing and do not publish the Intel DMG. CI should emit a clear blocker annotation and still allow Windows and arm64 artifacts to publish.

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
