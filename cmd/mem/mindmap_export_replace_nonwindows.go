//go:build !windows

package main

import "os"

func replaceClassicMindMapExportFile(source, target string) error {
	return os.Rename(source, target)
}
