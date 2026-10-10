# Project instructions

## Scope and efficient execution

- This repository is `mem-tool` (`github.com/knaprus-14/mem-tool`). Determine
  dependencies, architecture, and commands from this repository and `go.mod`.
- RAD Studio/Kai buffer and sequential-call rules apply only when working with
  files open in RAD Studio. Independent repository searches and read-only shell
  checks may run in parallel; dependent edits and deployment remain sequential.
- Inspect the working tree before edits and preserve existing user changes.
  Use focused `rg` searches and read the relevant files instead of dumping the
  entire repository into context.
- If a sandbox denies access to Go caches or deployment targets, report the
  exact blocked step. Use a permitted temporary `GOCACHE` when needed; do not
  describe a staged build as an installed release or bypass sandbox policy.

## GPT-6 Astra working guidelines

These rules adapt the [official model guidance](https://developers.openai.com/api/docs/guides/latest-model)
to Codex work on this repository. The active model and reasoning effort are
selected in Codex settings; this file does not switch the model. Inherit the
user's selection and scale planning and verification to the task's complexity.

- Infer routine implementation details from the request and existing code.
  Carry authorized work through implementation and relevant verification.
  Ask only when an answer materially changes the result, access is unavailable,
  or an action exceeds scope. Continue independent useful work while waiting.
- For multi-step work, keep a short plan with a concrete completion criterion.
  Incorporate corrections and answer side questions while preserving the
  original objective unless the user cancels or replaces it. After a context
  handoff, resume from completed work and recorded verification results.
- Apply explicit user instructions over general skill guidance, within system
  and platform constraints. If a skill causes a pause or scope change, identify
  the exact `SKILL.md` and quote the relevant rule. Do not infer extra approval
  requirements from general advice or ask again for authorization already given.
- Parallelize independent searches and read-only checks when useful. Use
  subagents only when explicitly requested or required by applicable instructions;
  give each a bounded independent task and avoid overlapping edits.
- Start with checks for the affected Go packages or changed workflow. Broaden
  verification when shared behavior, integration risks, or failures warrant it.
  Reproduce bugs using isolated temporary data; preserve real indexes, databases,
  and configuration. Do not add tests that merely mirror trivial edits or repeat
  passing checks without new evidence. Mandatory release checks below still apply.
- For instruction-only changes, review the diff and check formatting; a Go build
  and release are unnecessary unless application behavior also changes.
- Respond in Russian, leading with the result. Explain what changed, why, how it
  was verified, and any remaining limitation. Clearly distinguish implementation,
  successful tests, staged artifacts, and actual deployment. Keep progress updates
  focused on findings and next steps; use detail and lists only when helpful.

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
