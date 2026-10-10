package main

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"fmt"
	"strings"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
	ui "github.com/knaprus-14/mem-tool/pkg/ui"
)

func sourceReference(entry Entry) string {
	_, ref := mem.CitationForEntry(entry)
	return ref
}

func sortSearchResults(results []Entry)               { mem.SortSearchResults(results) }
func reRankResults(results []Entry, query string) int { return mem.ReRankResults(results, query) }

func handleRecent(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleRecent")(&applogResult0)

	if err := validateCommonFlags(args, "-limit"); err != nil {
		return err
	}
	positional, _, _, limit, _, _, _, _, _, _ := parseFlags(args)
	if len(positional) != 0 {
		return fmt.Errorf("лишний аргумент: %s", positional[0])
	}

	entries, err := store.Recent(limit)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	if len(entries) == 0 {
		fmt.Println(ui.Warn("База пуста. Начни с: %s", ui.Tag(`mem add "какой-то факт"`)))
		return nil
	}

	fmt.Println(ui.Header(fmt.Sprintf("Последние %d записей:", len(entries))))
	fmt.Println()
	for i, e := range entries {
		display := e.Title
		if display == "" {
			display = e.Text
			if len([]rune(display)) > 120 {
				display = string([]rune(display)[:120]) + "..."
			}
		}
		ref := ""
		if e.SourceFile != "" {
			ref = " " + ui.Tag(fmt.Sprintf("[%s]", e.SourceFile))
		}
		tagStr := ""
		if len(e.Tags) > 0 {
			tagStr = " " + ui.Tag("("+strings.Join(e.Tags, ", ")+")")
		}
		impMark := ""
		if e.Important {
			impMark = " " + ui.Mark("warn")
		}
		fmt.Printf("  %s%s%s%s  %s\n", ui.ID(fmt.Sprintf("#%d", e.ID)), tagStr, ref, impMark, display)
		if i < len(entries)-1 {
			fmt.Println(ui.Separator())
		}
	}
	return nil
}
