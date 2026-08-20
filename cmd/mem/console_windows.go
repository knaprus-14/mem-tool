//go:build windows

package main

import "syscall"

func init() {
	// Windows-консоль использует UTF-8 для корректного русского ввода и вывода.
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	kernel32.NewProc("SetConsoleOutputCP").Call(65001) // CP_UTF8
	kernel32.NewProc("SetConsoleCP").Call(65001)       // CP_UTF8
}
