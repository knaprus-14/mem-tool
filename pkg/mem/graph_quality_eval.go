package mem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	KnowledgeQualityManifestSchemaVersion = 1
	MaxKnowledgeQualityManifestBytes      = 1 << 20
	MaxKnowledgeQualityManifestNameRunes  = 256
)

var ErrKnowledgeQualityGateFailed = errors.New("knowledge quality gate failed")

// KnowledgeQualityManifest is a reproducible, model-free quality gate for a
// selected current corpus scope. Pointer thresholds distinguish an omitted
// check from an explicit zero.
type KnowledgeQualityManifest struct {
	SchemaVersion int                          `json:"schema_version"`
	Name          string                       `json:"name"`
	Scope         KnowledgeCoverageOptions     `json:"scope"`
	Requirements  KnowledgeQualityRequirements `json:"requirements"`
}

type KnowledgeQualityRequirements struct {
	MinImportCoveragePercent    *float64 `json:"min_import_coverage_percent,omitempty"`
	MinProcessingPercent        *float64 `json:"min_processing_percent,omitempty"`
	MinKnowledgeCoveragePercent *float64 `json:"min_knowledge_coverage_percent,omitempty"`
	MaxUnprocessedChunks        *int     `json:"max_unprocessed_chunks,omitempty"`
	MaxUncoveredChunks          *int     `json:"max_uncovered_chunks,omitempty"`
	MaxLowConfidenceOCRChunks   *int     `json:"max_low_confidence_ocr_chunks,omitempty"`
	MaxWarningChunks            *int     `json:"max_warning_chunks,omitempty"`
	MinExtractedNodes           *int     `json:"min_extracted_nodes,omitempty"`
	MinExtractedRelations       *int     `json:"min_extracted_relations,omitempty"`
	MaxDraftObjects             *int     `json:"max_draft_objects,omitempty"`
	MaxStaleEvidenceObjects     *int     `json:"max_stale_evidence_objects,omitempty"`
	MaxMissingEvidenceObjects   *int     `json:"max_missing_evidence_objects,omitempty"`
}

type KnowledgeQualityCheck struct {
	Metric   string  `json:"metric"`
	Operator string  `json:"operator"`
	Expected float64 `json:"expected"`
	Actual   float64 `json:"actual"`
	Unit     string  `json:"unit"`
	Passed   bool    `json:"passed"`
}

type KnowledgeQualityReport struct {
	SchemaVersion  int                      `json:"schema_version"`
	Name           string                   `json:"name"`
	ManifestDigest string                   `json:"manifest_digest"`
	SnapshotDigest string                   `json:"snapshot_digest"`
	Scope          KnowledgeCoverageOptions `json:"scope"`
	Summary        KnowledgeCoverageSummary `json:"summary"`
	Checks         []KnowledgeQualityCheck  `json:"checks"`
	Passed         bool                     `json:"passed"`
}

func ParseKnowledgeQualityManifest(data []byte) (KnowledgeQualityManifest, error) {
	if len(data) == 0 {
		return KnowledgeQualityManifest{}, errors.New("knowledge quality manifest is empty")
	}
	if len(data) > MaxKnowledgeQualityManifestBytes {
		return KnowledgeQualityManifest{}, fmt.Errorf("knowledge quality manifest exceeds %d bytes", MaxKnowledgeQualityManifestBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest KnowledgeQualityManifest
	if err := decoder.Decode(&manifest); err != nil {
		return KnowledgeQualityManifest{}, fmt.Errorf("decode knowledge quality manifest: %w", err)
	}
	if err := ensureKnowledgeQualityJSONEOF(decoder); err != nil {
		return KnowledgeQualityManifest{}, err
	}
	if err := validateKnowledgeQualityManifest(manifest); err != nil {
		return KnowledgeQualityManifest{}, err
	}
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Scope.Document = strings.TrimSpace(manifest.Scope.Document)
	manifest.Scope.Tag = strings.TrimSpace(manifest.Scope.Tag)
	return manifest, nil
}

func ensureKnowledgeQualityJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return errors.New("knowledge quality manifest must contain exactly one JSON object")
	}
	return fmt.Errorf("decode knowledge quality manifest trailing data: %w", err)
}

func validateKnowledgeQualityManifest(manifest KnowledgeQualityManifest) error {
	if manifest.SchemaVersion != KnowledgeQualityManifestSchemaVersion {
		return fmt.Errorf("knowledge quality manifest schema_version must be %d", KnowledgeQualityManifestSchemaVersion)
	}
	name := strings.TrimSpace(manifest.Name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxKnowledgeQualityManifestNameRunes || strings.ContainsAny(name, "\r\n\t") {
		return fmt.Errorf("knowledge quality manifest name must be one line containing 1..%d runes", MaxKnowledgeQualityManifestNameRunes)
	}
	if manifest.Scope.PageFrom < 0 || manifest.Scope.PageTo < 0 ||
		(manifest.Scope.PageFrom == 0) != (manifest.Scope.PageTo == 0) ||
		(manifest.Scope.PageFrom > 0 && manifest.Scope.PageFrom > manifest.Scope.PageTo) {
		return errors.New("knowledge quality manifest page range is invalid")
	}
	if manifest.Scope.LowConfidence < 0 || math.IsNaN(manifest.Scope.LowConfidence) || math.IsInf(manifest.Scope.LowConfidence, 0) || manifest.Scope.LowConfidence > 100 {
		return errors.New("knowledge quality manifest low confidence threshold must be 0..100")
	}
	requirements := manifest.Requirements
	if knowledgeQualityRequirementCount(requirements) == 0 {
		return errors.New("knowledge quality manifest must define at least one requirement")
	}
	for name, value := range map[string]*float64{
		"min_import_coverage_percent":    requirements.MinImportCoveragePercent,
		"min_processing_percent":         requirements.MinProcessingPercent,
		"min_knowledge_coverage_percent": requirements.MinKnowledgeCoveragePercent,
	} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 100) {
			return fmt.Errorf("knowledge quality requirement %s must be 0..100", name)
		}
	}
	for name, value := range map[string]*int{
		"max_unprocessed_chunks":        requirements.MaxUnprocessedChunks,
		"max_uncovered_chunks":          requirements.MaxUncoveredChunks,
		"max_low_confidence_ocr_chunks": requirements.MaxLowConfidenceOCRChunks,
		"max_warning_chunks":            requirements.MaxWarningChunks,
		"min_extracted_nodes":           requirements.MinExtractedNodes,
		"min_extracted_relations":       requirements.MinExtractedRelations,
		"max_draft_objects":             requirements.MaxDraftObjects,
		"max_stale_evidence_objects":    requirements.MaxStaleEvidenceObjects,
		"max_missing_evidence_objects":  requirements.MaxMissingEvidenceObjects,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("knowledge quality requirement %s must be non-negative", name)
		}
	}
	return nil
}

func knowledgeQualityRequirementCount(r KnowledgeQualityRequirements) int {
	count := 0
	for _, present := range []bool{
		r.MinImportCoveragePercent != nil, r.MinProcessingPercent != nil,
		r.MinKnowledgeCoveragePercent != nil, r.MaxUnprocessedChunks != nil,
		r.MaxUncoveredChunks != nil, r.MaxLowConfidenceOCRChunks != nil,
		r.MaxWarningChunks != nil, r.MinExtractedNodes != nil,
		r.MinExtractedRelations != nil, r.MaxDraftObjects != nil,
		r.MaxStaleEvidenceObjects != nil, r.MaxMissingEvidenceObjects != nil,
	} {
		if present {
			count++
		}
	}
	return count
}

// EvaluateKnowledgeQuality measures the selected current corpus and evaluates
// every declared gate. It does not call an embedding or answer model and does
// not mutate the store.
func (s *Store) EvaluateKnowledgeQuality(manifest KnowledgeQualityManifest, defaultLowConfidence float64) (KnowledgeQualityReport, error) {
	if err := validateKnowledgeQualityManifest(manifest); err != nil {
		return KnowledgeQualityReport{}, err
	}
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Scope.Document = strings.TrimSpace(manifest.Scope.Document)
	manifest.Scope.Tag = strings.TrimSpace(manifest.Scope.Tag)
	if manifest.Scope.LowConfidence == 0 {
		manifest.Scope.LowConfidence = defaultLowConfidence
	}
	coverage, err := s.BuildKnowledgeCoverageReport(manifest.Scope)
	if err != nil {
		return KnowledgeQualityReport{}, fmt.Errorf("build knowledge quality coverage: %w", err)
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return KnowledgeQualityReport{}, fmt.Errorf("encode knowledge quality manifest: %w", err)
	}
	digestBytes := sha256.Sum256(canonical)
	report := KnowledgeQualityReport{
		SchemaVersion: KnowledgeQualityManifestSchemaVersion,
		Name:          manifest.Name, ManifestDigest: "sha256:" + hex.EncodeToString(digestBytes[:]),
		SnapshotDigest: coverage.SnapshotDigest, Scope: coverage.Scope, Summary: coverage.Summary,
		Passed: true,
	}
	report.Checks = buildKnowledgeQualityChecks(manifest.Requirements, coverage.Summary)
	for _, check := range report.Checks {
		if !check.Passed {
			report.Passed = false
		}
	}
	return report, nil
}

func buildKnowledgeQualityChecks(r KnowledgeQualityRequirements, s KnowledgeCoverageSummary) []KnowledgeQualityCheck {
	checks := make([]KnowledgeQualityCheck, 0, knowledgeQualityRequirementCount(r))
	appendMin := func(metric string, expected *float64, actual float64, unit string) {
		if expected != nil {
			checks = append(checks, KnowledgeQualityCheck{Metric: metric, Operator: ">=", Expected: *expected, Actual: actual, Unit: unit, Passed: actual >= *expected})
		}
	}
	appendMax := func(metric string, expected *int, actual int) {
		if expected != nil {
			checks = append(checks, KnowledgeQualityCheck{Metric: metric, Operator: "<=", Expected: float64(*expected), Actual: float64(actual), Unit: "count", Passed: actual <= *expected})
		}
	}
	appendMinCount := func(metric string, expected *int, actual int) {
		if expected != nil {
			checks = append(checks, KnowledgeQualityCheck{Metric: metric, Operator: ">=", Expected: float64(*expected), Actual: float64(actual), Unit: "count", Passed: actual >= *expected})
		}
	}
	appendMin("import_coverage_percent", r.MinImportCoveragePercent, s.ImportCoveragePercent, "percent")
	appendMin("processing_percent", r.MinProcessingPercent, s.ProcessingPercent, "percent")
	appendMin("knowledge_coverage_percent", r.MinKnowledgeCoveragePercent, s.CoveragePercent, "percent")
	appendMax("unprocessed_chunks", r.MaxUnprocessedChunks, s.UnprocessedChunks)
	appendMax("uncovered_chunks", r.MaxUncoveredChunks, s.UncoveredChunks)
	appendMax("low_confidence_ocr_chunks", r.MaxLowConfidenceOCRChunks, s.LowConfidenceOCRChunks)
	appendMax("warning_chunks", r.MaxWarningChunks, s.WarningChunks)
	appendMinCount("extracted_nodes", r.MinExtractedNodes, s.ExtractedNodes)
	appendMinCount("extracted_relations", r.MinExtractedRelations, s.ExtractedRelations)
	appendMax("draft_objects", r.MaxDraftObjects, s.DraftObjects)
	appendMax("stale_evidence_objects", r.MaxStaleEvidenceObjects, s.StaleEvidenceObjects)
	appendMax("missing_evidence_objects", r.MaxMissingEvidenceObjects, s.MissingEvidenceObjects)
	return checks
}
