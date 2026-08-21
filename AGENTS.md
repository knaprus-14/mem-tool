# Project instructions

## Windows build and deployment

- After every successful Windows build of this repository, deploy the current
  user-facing binaries to `C:\Users\ZMII\bin` so that the PATH installation is
  never older than the source build.
- Build all three commands for `windows/amd64`: `mem.exe`, `mem-index.exe`, and
  `mem-bot.exe`.
- Use `deploy/windows.ps1` for the normal test, build, hash verification, smoke
  test, and deployment workflow.
- Run the relevant tests before deployment. Build into a temporary staging
  directory first; do not compile directly over a potentially running binary.
- Replace only the three explicitly named executables in the deployment
  directory. Preserve every other file in `C:\Users\ZMII\bin`.
- After copying, verify that all three files exist, compare their SHA-256 hashes
  with the staged artifacts, and smoke-test the non-secret version commands.
- Never copy `.env.local`, Telegram tokens, databases, generated maps, or other
  project data into the binary directory.
- A Linux `mem-bot` build is a separate VDS deployment described in
  `deploy/README.md`; do not place it in the Windows binary directory.

## Semantic versioning

- Follow `VERSIONING.md` and Semantic Versioning 2.0.0 for every user-visible
  change: MAJOR for incompatible changes, MINOR for backward-compatible
  features, and PATCH for backward-compatible fixes.
- `internal/buildinfo.Version` is the only source version for `mem`, `mem-index`,
  and `mem-bot`. Do not add per-command version literals.
- Decide and update the version in the same change that adds the feature or fix,
  before building and deploying it.
- Keep `DOCUMENTATION.md` release notes current. A successful build with an old
  release number is not complete and must not be deployed.

## User documentation

- Keep `USAGE_RU.md` aligned with user-visible commands and deployment paths.
