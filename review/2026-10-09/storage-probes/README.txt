Reproduce (isolated t.TempDir databases, fake embeddings; intentional regression failures):
$env:GOCACHE = Join-Path (Get-Location) 'build/go-cache'
$env:GOTELEMETRY = 'off'
go test -overlay ./review/2026-10-09/storage-probes/overlay.json ./pkg/mem -run '^TestReviewStorage' -count=1 -timeout=120s

No application package files were edited; the overlay injects these probes into pkg/mem only for the selected command.
