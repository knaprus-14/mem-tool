package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

const mapEvalUsage = "использование: mem map eval <manifest.json> [--json]"

func handleMapEval(cfg *Config, store *Store, args []string) error {
	manifestPath := ""
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "--json":
			if jsonOutput {
				return errors.New("map eval: --json указан повторно")
			}
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("map eval: неизвестный аргумент %s", arg)
			}
			if manifestPath != "" {
				return errors.New(mapEvalUsage)
			}
			manifestPath = arg
		}
	}
	if strings.TrimSpace(manifestPath) == "" {
		return errors.New(mapEvalUsage)
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("map eval: открыть manifest: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, mem.MaxKnowledgeQualityManifestBytes+1))
	if err != nil {
		return fmt.Errorf("map eval: прочитать manifest: %w", err)
	}
	manifest, err := mem.ParseKnowledgeQualityManifest(data)
	if err != nil {
		return fmt.Errorf("map eval: %w", err)
	}
	report, err := store.EvaluateKnowledgeQuality(manifest, cfg.Ingest.LowConfidence)
	if err != nil {
		return fmt.Errorf("map eval: %w", err)
	}
	if jsonOutput {
		encoded, encodeErr := json.MarshalIndent(report, "", "  ")
		if encodeErr != nil {
			return fmt.Errorf("map eval: encode report: %w", encodeErr)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
	} else {
		printKnowledgeQualityReport(report)
	}
	if !report.Passed {
		failed := 0
		for _, check := range report.Checks {
			if !check.Passed {
				failed++
			}
		}
		return fmt.Errorf("%w: не пройдено %d из %d проверок", mem.ErrKnowledgeQualityGateFailed, failed, len(report.Checks))
	}
	return nil
}

func printKnowledgeQualityReport(report mem.KnowledgeQualityReport) {
	result := "PASS"
	if !report.Passed {
		result = "FAIL"
	}
	fmt.Fprintln(os.Stdout, "Воспроизводимая проверка качества карты")
	fmt.Fprintln(os.Stdout, "--------------------------------------")
	fmt.Fprintf(os.Stdout, "Набор: %s\n", report.Name)
	fmt.Fprintf(os.Stdout, "Результат: %s\n", result)
	fmt.Fprintf(os.Stdout, "Manifest: %s\n", report.ManifestDigest)
	fmt.Fprintf(os.Stdout, "Снимок:   %s\n\n", report.SnapshotDigest)
	for _, check := range report.Checks {
		mark := "[OK]"
		if !check.Passed {
			mark = "[FAIL]"
		}
		if check.Unit == "percent" {
			fmt.Fprintf(os.Stdout, "%s %-34s %.2f %s %.2f %%\n", mark, check.Metric, check.Actual, check.Operator, check.Expected)
		} else {
			fmt.Fprintf(os.Stdout, "%s %-34s %.0f %s %.0f\n", mark, check.Metric, check.Actual, check.Operator, check.Expected)
		}
	}
	fmt.Fprintf(os.Stdout, "\nКорпус: документов %d; chunks %d; узлов %d; связей %d; OCR low-confidence %d; stale/missing %d/%d\n",
		report.Summary.Documents, report.Summary.ChunksWithText, report.Summary.ExtractedNodes,
		report.Summary.ExtractedRelations, report.Summary.LowConfidenceOCRChunks,
		report.Summary.StaleEvidenceObjects, report.Summary.MissingEvidenceObjects)
}
