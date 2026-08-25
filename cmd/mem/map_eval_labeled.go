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

const mapEvalLabeledUsage = "использование: mem map eval-labeled <manifest.json> [--json]"

func handleMapEvalLabeled(store *Store, args []string) error {
	manifestPath := ""
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "--json":
			if jsonOutput {
				return errors.New("map eval-labeled: --json указан повторно")
			}
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("map eval-labeled: неизвестный аргумент %s", arg)
			}
			if manifestPath != "" {
				return errors.New(mapEvalLabeledUsage)
			}
			manifestPath = arg
		}
	}
	if strings.TrimSpace(manifestPath) == "" {
		return errors.New(mapEvalLabeledUsage)
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("map eval-labeled: открыть manifest: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, mem.MaxKnowledgeLabeledEvalManifestBytes+1))
	if err != nil {
		return fmt.Errorf("map eval-labeled: прочитать manifest: %w", err)
	}
	manifest, err := mem.ParseKnowledgeLabeledEvalManifest(data)
	if err != nil {
		return fmt.Errorf("map eval-labeled: %w", err)
	}
	report, err := store.EvaluateKnowledgeLabeledQuality(manifest)
	if err != nil {
		return fmt.Errorf("map eval-labeled: %w", err)
	}
	if jsonOutput {
		encoded, encodeErr := json.MarshalIndent(report, "", "  ")
		if encodeErr != nil {
			return fmt.Errorf("map eval-labeled: encode report: %w", encodeErr)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
	} else {
		printKnowledgeLabeledEvalReport(report)
	}
	if !report.Passed {
		failed := 0
		for _, check := range report.Checks {
			if !check.Passed {
				failed++
			}
		}
		return fmt.Errorf("%w: не пройдено %d из %d проверок", mem.ErrKnowledgeLabeledEvalFailed, failed, len(report.Checks))
	}
	return nil
}

func printKnowledgeLabeledEvalReport(report mem.KnowledgeLabeledEvalReport) {
	result := "PASS"
	if !report.Passed {
		result = "FAIL"
	}
	fmt.Fprintln(os.Stdout, "Размеченная оценка карты знаний")
	fmt.Fprintln(os.Stdout, "--------------------------------")
	fmt.Fprintf(os.Stdout, "Набор: %s\n", report.Name)
	fmt.Fprintf(os.Stdout, "Результат: %s\n", result)
	fmt.Fprintf(os.Stdout, "Manifest: %s\n", report.ManifestDigest)
	fmt.Fprintf(os.Stdout, "Снимок:   %s\n", report.SnapshotDigest)
	fmt.Fprintf(os.Stdout, "Claims: ожидается %d · найдено %d · TP %d · FP %d · FN %d · precision %.2f%% · recall %.2f%% · F1 %.2f%%\n",
		report.Claims.Expected, report.Claims.Predicted, report.Claims.TruePositive,
		report.Claims.FalsePositive, report.Claims.FalseNegative,
		report.Claims.Precision, report.Claims.Recall, report.Claims.F1)
	fmt.Fprintf(os.Stdout, "Contradictions: ожидается %d · найдено %d · TP %d · FP %d · FN %d · precision %.2f%% · recall %.2f%% · F1 %.2f%%\n",
		report.Contradictions.Expected, report.Contradictions.Predicted, report.Contradictions.TruePositive,
		report.Contradictions.FalsePositive, report.Contradictions.FalseNegative,
		report.Contradictions.Precision, report.Contradictions.Recall, report.Contradictions.F1)
	if report.ExcludedNonCurrentClaims > 0 || report.ExcludedNonCurrentContradictions > 0 {
		fmt.Fprintf(os.Stdout, "Исключено без current evidence: claims %d · contradictions %d\n",
			report.ExcludedNonCurrentClaims, report.ExcludedNonCurrentContradictions)
	}
	fmt.Fprintln(os.Stdout)
	for _, check := range report.Checks {
		mark := "[OK]"
		if !check.Passed {
			mark = "[FAIL]"
		}
		if check.Unit == "percent" {
			fmt.Fprintf(os.Stdout, "%s %-38s %.2f %s %.2f %%\n", mark, check.Metric, check.Actual, check.Operator, check.Expected)
		} else {
			fmt.Fprintf(os.Stdout, "%s %-38s %.0f %s %.0f\n", mark, check.Metric, check.Actual, check.Operator, check.Expected)
		}
	}
	if len(report.FalsePositiveClaims) > 0 {
		fmt.Fprintln(os.Stdout, "\nЛишние claims (FP):")
		for _, claim := range report.FalsePositiveClaims {
			fmt.Fprintf(os.Stdout, "- %s — %s%s\n", claim.NodeID, claim.Label, knowledgeLabeledHumanEvidence(claim.Evidence))
		}
	}
	if len(report.FalseNegativeClaims) > 0 {
		fmt.Fprintln(os.Stdout, "\nПропущенные claims (FN):")
		for _, claim := range report.FalseNegativeClaims {
			fmt.Fprintf(os.Stdout, "- %s — %s\n", claim.ID, claim.Text)
		}
	}
	if len(report.FalsePositiveContradictions) > 0 {
		fmt.Fprintln(os.Stdout, "\nЛишние contradictions (FP):")
		for _, item := range report.FalsePositiveContradictions {
			fmt.Fprintf(os.Stdout, "- %s — %s ↔ %s%s\n", item.EdgeID, item.FromLabel, item.ToLabel, knowledgeLabeledHumanEvidence(item.Evidence))
		}
	}
	if len(report.FalseNegativeContradictions) > 0 {
		fmt.Fprintln(os.Stdout, "\nПропущенные contradictions (FN):")
		for _, item := range report.FalseNegativeContradictions {
			fmt.Fprintf(os.Stdout, "- %s — %s ↔ %s\n", item.ID, item.LeftClaimID, item.RightClaimID)
		}
	}
}

func knowledgeLabeledHumanEvidence(evidence []mem.KnowledgeLabeledEvidenceRef) string {
	if len(evidence) == 0 {
		return ""
	}
	item := evidence[0]
	location := strings.TrimSpace(item.SourcePath)
	if item.Page > 0 {
		location += fmt.Sprintf(" · стр. %d", item.Page)
	}
	if location == "" {
		return ""
	}
	if len(evidence) > 1 {
		return fmt.Sprintf(" [%s; источников %d]", location, len(evidence))
	}
	return " [" + location + "]"
}
