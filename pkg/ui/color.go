// Package ui provides colored output helpers for mem-tool CLI.
//
// Color behavior:
//   - Auto-disabled when stdout is not a TTY (e.g., when piped).
//   - Honors NO_COLOR (https://no-color.org/) and MEM_NO_COLOR=1 environment variables.
//   - Can be forced via Init("always" | "never") or the --color CLI flag.
//
// When colors are off, all functions return their input strings unchanged.
package ui

import (
	"fmt"
	"os"
	"sync"

	"github.com/fatih/color"
	"golang.org/x/term"
)

var (
	once       sync.Once
	colorIsOn  bool
)

// Init configures the global color behavior.
//
// mode can be: "always", "never", or "auto" (default).
// In auto mode, color is enabled only when stdout is a terminal
// and no disable env vars are set.
//
// Set MEM_UI_DEBUG=1 to print detection info to stderr.
func Init(mode string) {
	once.Do(func() {
		colorIsOn = determineMode(mode)
		color.NoColor = !colorIsOn
		if os.Getenv("MEM_UI_DEBUG") != "" {
			fmt.Fprintf(os.Stderr,
				"[ui-debug] mode=%q → enabled=%v (NO_COLOR=%q MEM_NO_COLOR=%q COLORTERM=%q TERM=%q isatty=%v)\n",
				mode, colorIsOn,
				os.Getenv("NO_COLOR"), os.Getenv("MEM_NO_COLOR"),
				os.Getenv("COLORTERM"), os.Getenv("TERM"),
				term.IsTerminal(int(os.Stdout.Fd())),
			)
		}
	})
}

func determineMode(mode string) bool {
	switch mode {
	case "always", "yes", "force":
		return true
	case "never", "no", "off":
		return false
	}

	// auto mode
	if os.Getenv("NO_COLOR") != "" {
		return false // standard: any non-empty value disables color
	}
	if os.Getenv("MEM_NO_COLOR") != "" {
		return false
	}

	// Detect TTY
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// Enabled returns whether colors are currently active.
func Enabled() bool { return colorIsOn }

// ID formats a record ID like "#50" in bold yellow.
func ID(s string) string {
	if !colorIsOn {
		return s
	}
	return color.RGB(245, 215, 110).Add(color.Bold).Sprint(s)
}

// Score formats a percentage (0-100) with color thresholds:
//   - >=100%  green bold   (an exact/full-strength match)
//   - 70-99%  yellow bold  (good match)
//   - <70%    red          (weak match)
func Score(percent int) string {
	text := fmt.Sprintf("%d%%", percent)
	if !colorIsOn {
		return text
	}
	switch {
	case percent >= 100:
		return color.RGB(125, 220, 125).Add(color.Bold).Sprint(text)
	case percent >= 70:
		return color.RGB(245, 215, 110).Add(color.Bold).Sprint(text)
	default:
		return color.RGB(220, 110, 110).Sprint(text)
	}
}

// Tag formats a single tag in cyan.
func Tag(s string) string {
	if !colorIsOn {
		return s
	}
	return color.RGB(125, 196, 220).Sprint(s)
}

// Date formats a date string in gray.
func Date(s string) string {
	if !colorIsOn {
		return s
	}
	return color.RGB(136, 136, 136).Sprint(s)
}

// Header formats a section header in magenta bold.
func Header(s string) string {
	if !colorIsOn {
		return s
	}
	return color.RGB(220, 125, 196).Add(color.Bold).Sprint(s)
}

// Separator prints a horizontal divider line.
func Separator() string {
	if !colorIsOn {
		return "──────────────────────────────────────────────────"
	}
	return color.RGB(68, 68, 68).Sprint("──────────────────────────────────────────────────")
}

// Mark returns a colored mark symbol based on match strength.
// kind: "good" (green ✓), "mid" (yellow ~), "low" (red ),
// "ok" (green ✓ prefix), "warn" (yellow ⚠ prefix), "err" (red ✗ prefix)
func Mark(kind string) string {
	if !colorIsOn {
		switch kind {
		case "good":
			return "[*]"
		case "mid":
			return "[~]"
		case "low":
			return "[ ]"
		case "ok":
			return "[OK]"
		case "warn":
			return "[WARN]"
		case "err":
			return "[ERR]"
		}
		return ""
	}
	switch kind {
	case "good":
		return color.RGB(125, 220, 125).Add(color.Bold).Sprint("[*]")
	case "mid":
		return color.RGB(245, 215, 110).Add(color.Bold).Sprint("[~]")
	case "low":
		return color.RGB(120, 120, 120).Sprint("[ ]")
	case "ok":
		return color.RGB(125, 220, 125).Add(color.Bold).Sprint("[OK]")
	case "warn":
		return color.RGB(245, 215, 110).Add(color.Bold).Sprint("[WARN]")
	case "err":
		return color.RGB(220, 110, 110).Add(color.Bold).Sprint("[ERR]")
	}
	return ""
}

// Key formats a label/key string (e.g. "Backend:", "Model:") in dim gray.
func Key(s string) string {
	if !colorIsOn {
		return s
	}
	return color.RGB(160, 160, 160).Sprint(s)
}

// Value formats a value string with default styling (white).
func Value(s string) string {
	if !colorIsOn {
		return s
	}
	return color.RGB(230, 230, 230).Sprint(s)
}

// Number formats a numeric value in bright white.
func Number(s string) string {
	if !colorIsOn {
		return s
	}
	return color.RGB(255, 255, 255).Add(color.Bold).Sprint(s)
}

// Success returns a success-prefixed message: ✓ text in green bold.
func Success(format string, args ...interface{}) string {
	s := fmt.Sprintf(format, args...)
	if !colorIsOn {
		return "✓ " + s
	}
	return color.RGB(125, 220, 125).Add(color.Bold).Sprint("✓ ") + s
}

// Warn returns a warning-prefixed message: ⚠ text in yellow bold.
func Warn(format string, args ...interface{}) string {
	s := fmt.Sprintf(format, args...)
	if !colorIsOn {
		return "⚠ " + s
	}
	return color.RGB(245, 215, 110).Add(color.Bold).Sprint("⚠ ") + s
}

// Err returns an error-prefixed message: ✗ text in red bold.
func Err(format string, args ...interface{}) string {
	s := fmt.Sprintf(format, args...)
	if !colorIsOn {
		return "✗ " + s
	}
	return color.RGB(220, 110, 110).Add(color.Bold).Sprint("✗ ") + s
}
