package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

const mapProfileUsage = "использование: mem map profile [-iterations N] [--view <имя>] [--json]"

func handleMapProfile(store *Store, args []string) error {
	options := mem.KnowledgeMapProfileOptions{}
	jsonOutput := false
	iterationsSet, viewSet := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			if jsonOutput {
				return errors.New("map profile: --json указан повторно")
			}
			jsonOutput = true
		case "-iterations", "--iterations":
			if iterationsSet || i+1 >= len(args) {
				return errors.New(mapProfileUsage)
			}
			iterationsSet = true
			i++
			value, err := strconv.Atoi(args[i])
			if err != nil || value < 1 || value > mem.MaxKnowledgeMapProfileIterations {
				return fmt.Errorf("map profile: iterations должен быть от 1 до %d", mem.MaxKnowledgeMapProfileIterations)
			}
			options.Iterations = value
		case "--view":
			if viewSet || i+1 >= len(args) {
				return errors.New(mapProfileUsage)
			}
			viewSet = true
			i++
			options.ViewName = strings.TrimSpace(args[i])
			if options.ViewName == "" {
				return errors.New("map profile: --view не должен быть пустым")
			}
		default:
			return fmt.Errorf("map profile: неизвестный аргумент %s", args[i])
		}
	}
	report, err := store.ProfileKnowledgeMap(options)
	if err != nil {
		return fmt.Errorf("map profile: %w", err)
	}
	if jsonOutput {
		encoded, encodeErr := json.MarshalIndent(report, "", "  ")
		if encodeErr != nil {
			return fmt.Errorf("map profile: encode report: %w", encodeErr)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	printKnowledgeMapProfileReport(report)
	return nil
}

func printKnowledgeMapProfileReport(report mem.KnowledgeMapProfileReport) {
	fmt.Fprintln(os.Stdout, "Профиль производительности карты знаний")
	fmt.Fprintln(os.Stdout, "----------------------------------------")
	fmt.Fprintf(os.Stdout, "Снимок графа:    %s\n", report.Snapshot.GraphDigest)
	fmt.Fprintf(os.Stdout, "Состояние evidence: %s\n", report.Snapshot.EvidenceStateDigest)
	fmt.Fprintf(os.Stdout, "База: %s\n", report.Database.Path)
	fmt.Fprintf(os.Stdout, "Размеры: DB %s · WAL %s · SHM %s · SQLite pages %d × %d байт · свободно %d стр. · data_version %d\n",
		humanProfileBytes(report.Database.MainBytes), humanProfileBytes(report.Database.WALBytes),
		humanProfileBytes(report.Database.SHMBytes), report.Database.PageCount, report.Database.PageSize,
		report.Database.FreelistPages, report.Database.DataVersion)
	fmt.Fprintf(os.Stdout, "Данные: документов %d · entries %d · узлов %d · связей %d · evidence %d (current/stale/missing %d/%d/%d)\n",
		report.Database.Documents, report.Database.Entries, report.Snapshot.Nodes, report.Snapshot.Edges,
		report.Snapshot.Evidence, report.Snapshot.CurrentEvidence, report.Snapshot.StaleEvidence, report.Snapshot.MissingEvidence)
	fmt.Fprintf(os.Stdout, "Среда: %s/%s · %s · CPU %d · итераций %d · вид %s\n\n",
		report.Environment.GOOS, report.Environment.GOARCH, report.Environment.GoVersion,
		report.Environment.LogicalCPUs, report.Options.Iterations, report.Options.ViewName)
	for _, stage := range report.Stages {
		fmt.Fprintf(os.Stdout, "%-26s median %9s · p95 %9s · min %9s · max %9s\n",
			stage.Name, humanProfileDuration(stage.MedianNS), humanProfileDuration(stage.P95NS),
			humanProfileDuration(stage.MinNS), humanProfileDuration(stage.MaxNS))
	}
	fmt.Fprintf(os.Stdout, "\nHTML: %s · %s\n", humanProfileBytes(report.Snapshot.HTMLBytes), report.Snapshot.HTMLDigest)
	fmt.Fprintln(os.Stdout, "Ограничения:")
	for _, limitation := range report.Limitations {
		fmt.Fprintln(os.Stdout, "- "+limitation)
	}
}

func humanProfileDuration(nanoseconds int64) string {
	return time.Duration(nanoseconds).Round(time.Microsecond).String()
}

func humanProfileBytes(bytes int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case bytes >= gib:
		return fmt.Sprintf("%.2f GiB", float64(bytes)/gib)
	case bytes >= mib:
		return fmt.Sprintf("%.2f MiB", float64(bytes)/mib)
	case bytes >= kib:
		return fmt.Sprintf("%.2f KiB", float64(bytes)/kib)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
