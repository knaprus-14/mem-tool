// Package buildinfo contains release metadata shared by every mem-tool binary.
package buildinfo

// Version is the canonical semantic version for the whole mem-tool release.
//
// Release builds may override it with:
//
//	go build -ldflags "-X github.com/knaprus-14/mem-tool/internal/buildinfo.Version=1.41.0"
//
// Keep the default in sync with the release notes before committing a release.
var Version = "1.41.0"
