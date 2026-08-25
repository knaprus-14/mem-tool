package mem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	KnowledgeLabeledEvalSchemaVersion         = 1
	MaxKnowledgeLabeledEvalManifestBytes      = 8 << 20
	MaxKnowledgeLabeledEvalClaims             = 2000
	MaxKnowledgeLabeledEvalContradictions     = 5000
	MaxKnowledgeLabeledEvalAliases            = 32
	MaxKnowledgeLabeledEvalCitations          = 64
	MaxKnowledgeLabeledEvalClaimTextRunes     = 4096
	DefaultKnowledgeLabeledClaimSimilarity    = 0.80
	MaxKnowledgeLabeledEvalReportExcerptRunes = 240
)

var ErrKnowledgeLabeledEvalFailed = errors.New("knowledge labeled evaluation failed")

// KnowledgeLabeledEvalManifest defines a deterministic, model-free benchmark
// against human-annotated claims and contradiction pairs.
type KnowledgeLabeledEvalManifest struct {
	SchemaVersion  int                                     `json:"schema_version"`
	Name           string                                  `json:"name"`
	Scope          KnowledgeCoverageOptions                `json:"scope"`
	Matching       KnowledgeLabeledEvalMatching            `json:"matching"`
	Claims         []KnowledgeLabeledExpectedClaim         `json:"claims"`
	Contradictions []KnowledgeLabeledExpectedContradiction `json:"contradictions"`
	Requirements   KnowledgeLabeledEvalRequirements        `json:"requirements"`
}

type KnowledgeLabeledEvalMatching struct {
	ClaimSimilarityThreshold *float64          `json:"claim_similarity_threshold,omitempty"`
	Statuses                 []KnowledgeStatus `json:"statuses,omitempty"`
}

type KnowledgeLabeledExpectedClaim struct {
	ID                  string   `json:"id"`
	Text                string   `json:"text"`
	Aliases             []string `json:"aliases,omitempty"`
	NodeID              string   `json:"node_id,omitempty"`
	AcceptedCitationIDs []string `json:"accepted_citation_ids,omitempty"`
}

type KnowledgeLabeledExpectedContradiction struct {
	ID           string `json:"id"`
	LeftClaimID  string `json:"left_claim_id"`
	RightClaimID string `json:"right_claim_id"`
}

type KnowledgeLabeledEvalRequirements struct {
	MinClaimPrecision              *float64 `json:"min_claim_precision,omitempty"`
	MinClaimRecall                 *float64 `json:"min_claim_recall,omitempty"`
	MinClaimF1                     *float64 `json:"min_claim_f1,omitempty"`
	MinContradictionPrecision      *float64 `json:"min_contradiction_precision,omitempty"`
	MinContradictionRecall         *float64 `json:"min_contradiction_recall,omitempty"`
	MinContradictionF1             *float64 `json:"min_contradiction_f1,omitempty"`
	MaxFalsePositiveClaims         *int     `json:"max_false_positive_claims,omitempty"`
	MaxFalseNegativeClaims         *int     `json:"max_false_negative_claims,omitempty"`
	MaxFalsePositiveContradictions *int     `json:"max_false_positive_contradictions,omitempty"`
	MaxFalseNegativeContradictions *int     `json:"max_false_negative_contradictions,omitempty"`
}

type KnowledgeLabeledMetrics struct {
	Expected      int     `json:"expected"`
	Predicted     int     `json:"predicted"`
	TruePositive  int     `json:"true_positive"`
	FalsePositive int     `json:"false_positive"`
	FalseNegative int     `json:"false_negative"`
	Precision     float64 `json:"precision_percent"`
	Recall        float64 `json:"recall_percent"`
	F1            float64 `json:"f1_percent"`
}

type KnowledgeLabeledEvidenceRef struct {
	CitationID       string `json:"citation_id"`
	SourcePath       string `json:"source_path"`
	Page             int    `json:"page"`
	BlockIndex       int    `json:"block_index"`
	BlockChunkIndex  int    `json:"block_chunk_index"`
	DocumentRevision string `json:"document_revision"`
	ChunkHash        string `json:"chunk_hash"`
	Excerpt          string `json:"excerpt"`
}

type KnowledgeLabeledClaimPrediction struct {
	NodeID   string                        `json:"node_id"`
	Label    string                        `json:"label"`
	Body     string                        `json:"body,omitempty"`
	Status   KnowledgeStatus               `json:"status"`
	Evidence []KnowledgeLabeledEvidenceRef `json:"evidence"`
}

type KnowledgeLabeledClaimMatch struct {
	ExpectedID string                          `json:"expected_id"`
	NodeID     string                          `json:"node_id"`
	Similarity float64                         `json:"similarity"`
	Prediction KnowledgeLabeledClaimPrediction `json:"prediction"`
}

type KnowledgeLabeledContradictionPrediction struct {
	EdgeID     string                        `json:"edge_id"`
	Label      string                        `json:"label,omitempty"`
	Status     KnowledgeStatus               `json:"status"`
	FromNodeID string                        `json:"from_node_id"`
	FromLabel  string                        `json:"from_label"`
	ToNodeID   string                        `json:"to_node_id"`
	ToLabel    string                        `json:"to_label"`
	Evidence   []KnowledgeLabeledEvidenceRef `json:"evidence"`
}

type KnowledgeLabeledContradictionMatch struct {
	ExpectedID string                                  `json:"expected_id"`
	EdgeID     string                                  `json:"edge_id"`
	Prediction KnowledgeLabeledContradictionPrediction `json:"prediction"`
}

type KnowledgeLabeledEvalReport struct {
	SchemaVersion                    int                                       `json:"schema_version"`
	Name                             string                                    `json:"name"`
	ManifestDigest                   string                                    `json:"manifest_digest"`
	GraphDigest                      string                                    `json:"graph_digest"`
	EvidenceStateDigest              string                                    `json:"evidence_state_digest"`
	SnapshotDigest                   string                                    `json:"snapshot_digest"`
	Scope                            KnowledgeCoverageOptions                  `json:"scope"`
	Matching                         KnowledgeLabeledEvalMatching              `json:"matching"`
	Claims                           KnowledgeLabeledMetrics                   `json:"claim_metrics"`
	Contradictions                   KnowledgeLabeledMetrics                   `json:"contradiction_metrics"`
	ClaimMatches                     []KnowledgeLabeledClaimMatch              `json:"claim_matches"`
	FalsePositiveClaims              []KnowledgeLabeledClaimPrediction         `json:"false_positive_claims"`
	FalseNegativeClaims              []KnowledgeLabeledExpectedClaim           `json:"false_negative_claims"`
	ContradictionMatches             []KnowledgeLabeledContradictionMatch      `json:"contradiction_matches"`
	FalsePositiveContradictions      []KnowledgeLabeledContradictionPrediction `json:"false_positive_contradictions"`
	FalseNegativeContradictions      []KnowledgeLabeledExpectedContradiction   `json:"false_negative_contradictions"`
	ExcludedNonCurrentClaims         int                                       `json:"excluded_non_current_claims"`
	ExcludedNonCurrentContradictions int                                       `json:"excluded_non_current_contradictions"`
	Checks                           []KnowledgeQualityCheck                   `json:"checks"`
	Passed                           bool                                      `json:"passed"`
}

type knowledgeLabeledEvalSnapshot struct {
	graphDigest            string
	evidenceStateDigest    string
	snapshotDigest         string
	claims                 []KnowledgeLabeledClaimPrediction
	contradictions         []KnowledgeLabeledContradictionPrediction
	excludedClaims         int
	excludedContradictions int
}

// ParseKnowledgeLabeledEvalManifest decodes one strict JSON object, applies
// explicit defaults and returns a canonical ordering suitable for hashing.
func ParseKnowledgeLabeledEvalManifest(data []byte) (KnowledgeLabeledEvalManifest, error) {
	if len(data) == 0 {
		return KnowledgeLabeledEvalManifest{}, errors.New("knowledge labeled eval manifest is empty")
	}
	if len(data) > MaxKnowledgeLabeledEvalManifestBytes {
		return KnowledgeLabeledEvalManifest{}, fmt.Errorf("knowledge labeled eval manifest exceeds %d bytes", MaxKnowledgeLabeledEvalManifestBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest KnowledgeLabeledEvalManifest
	if err := decoder.Decode(&manifest); err != nil {
		return KnowledgeLabeledEvalManifest{}, fmt.Errorf("decode knowledge labeled eval manifest: %w", err)
	}
	if err := ensureKnowledgeLabeledEvalJSONEOF(decoder); err != nil {
		return KnowledgeLabeledEvalManifest{}, err
	}
	manifest = normalizeKnowledgeLabeledEvalManifest(manifest)
	if err := validateKnowledgeLabeledEvalManifest(manifest); err != nil {
		return KnowledgeLabeledEvalManifest{}, err
	}
	return manifest, nil
}

func ensureKnowledgeLabeledEvalJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return errors.New("knowledge labeled eval manifest must contain exactly one JSON object")
	}
	return fmt.Errorf("decode knowledge labeled eval manifest trailing data: %w", err)
}

func normalizeKnowledgeLabeledEvalManifest(manifest KnowledgeLabeledEvalManifest) KnowledgeLabeledEvalManifest {
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Scope.Document = strings.TrimSpace(manifest.Scope.Document)
	manifest.Scope.Tag = strings.TrimSpace(manifest.Scope.Tag)
	if manifest.Matching.ClaimSimilarityThreshold == nil {
		value := DefaultKnowledgeLabeledClaimSimilarity
		manifest.Matching.ClaimSimilarityThreshold = &value
	}
	if manifest.Matching.Statuses == nil {
		manifest.Matching.Statuses = []KnowledgeStatus{KnowledgeStatusActive, KnowledgeStatusDraft}
	}
	sort.Slice(manifest.Matching.Statuses, func(i, j int) bool { return manifest.Matching.Statuses[i] < manifest.Matching.Statuses[j] })
	for i := range manifest.Claims {
		claim := &manifest.Claims[i]
		claim.ID = strings.TrimSpace(claim.ID)
		claim.Text = strings.TrimSpace(claim.Text)
		claim.NodeID = strings.TrimSpace(claim.NodeID)
		for j := range claim.Aliases {
			claim.Aliases[j] = strings.TrimSpace(claim.Aliases[j])
		}
		for j := range claim.AcceptedCitationIDs {
			claim.AcceptedCitationIDs[j] = strings.TrimSpace(claim.AcceptedCitationIDs[j])
		}
		sort.Strings(claim.Aliases)
		sort.Strings(claim.AcceptedCitationIDs)
	}
	for i := range manifest.Contradictions {
		item := &manifest.Contradictions[i]
		item.ID = strings.TrimSpace(item.ID)
		item.LeftClaimID = strings.TrimSpace(item.LeftClaimID)
		item.RightClaimID = strings.TrimSpace(item.RightClaimID)
	}
	sort.Slice(manifest.Claims, func(i, j int) bool { return manifest.Claims[i].ID < manifest.Claims[j].ID })
	sort.Slice(manifest.Contradictions, func(i, j int) bool { return manifest.Contradictions[i].ID < manifest.Contradictions[j].ID })
	return manifest
}

func validateKnowledgeLabeledEvalManifest(manifest KnowledgeLabeledEvalManifest) error {
	if manifest.SchemaVersion != KnowledgeLabeledEvalSchemaVersion {
		return fmt.Errorf("knowledge labeled eval manifest schema_version must be %d", KnowledgeLabeledEvalSchemaVersion)
	}
	if manifest.Name == "" || !utf8.ValidString(manifest.Name) || utf8.RuneCountInString(manifest.Name) > MaxKnowledgeQualityManifestNameRunes || strings.ContainsAny(manifest.Name, "\r\n\t") {
		return fmt.Errorf("knowledge labeled eval manifest name must be one line containing 1..%d runes", MaxKnowledgeQualityManifestNameRunes)
	}
	if manifest.Scope.PageFrom < 0 || manifest.Scope.PageTo < 0 ||
		(manifest.Scope.PageFrom == 0) != (manifest.Scope.PageTo == 0) ||
		(manifest.Scope.PageFrom > 0 && manifest.Scope.PageFrom > manifest.Scope.PageTo) {
		return errors.New("knowledge labeled eval page range is invalid")
	}
	if manifest.Scope.LowConfidence != 0 {
		return errors.New("knowledge labeled eval scope does not use low_confidence_threshold")
	}
	if len(manifest.Claims) > MaxKnowledgeLabeledEvalClaims || len(manifest.Contradictions) > MaxKnowledgeLabeledEvalContradictions {
		return fmt.Errorf("knowledge labeled eval exceeds claim/contradiction limits %d/%d", MaxKnowledgeLabeledEvalClaims, MaxKnowledgeLabeledEvalContradictions)
	}
	threshold := manifest.Matching.ClaimSimilarityThreshold
	if threshold == nil || math.IsNaN(*threshold) || math.IsInf(*threshold, 0) || *threshold <= 0 || *threshold > 1 {
		return errors.New("knowledge labeled eval claim similarity threshold must be greater than 0 and at most 1")
	}
	if len(manifest.Matching.Statuses) == 0 {
		return errors.New("knowledge labeled eval matching.statuses must not be empty")
	}
	for i, status := range manifest.Matching.Statuses {
		if !validKnowledgeStatus(status) || status == KnowledgeStatusRejected {
			return fmt.Errorf("knowledge labeled eval status %q is not evaluable", status)
		}
		if i > 0 && manifest.Matching.Statuses[i-1] == status {
			return fmt.Errorf("knowledge labeled eval status %q is duplicated", status)
		}
	}
	claimIDs := make(map[string]bool, len(manifest.Claims))
	for _, claim := range manifest.Claims {
		if err := validateKnowledgeID(claim.ID); err != nil {
			return fmt.Errorf("knowledge labeled expected claim ID: %w", err)
		}
		if claimIDs[claim.ID] {
			return fmt.Errorf("knowledge labeled expected claim ID %q is duplicated", claim.ID)
		}
		claimIDs[claim.ID] = true
		if err := validateKnowledgeLabeledText("claim text", claim.Text); err != nil {
			return fmt.Errorf("knowledge labeled expected claim %q: %w", claim.ID, err)
		}
		if len(claim.Aliases) > MaxKnowledgeLabeledEvalAliases || len(claim.AcceptedCitationIDs) > MaxKnowledgeLabeledEvalCitations {
			return fmt.Errorf("knowledge labeled expected claim %q exceeds alias/citation limits %d/%d", claim.ID, MaxKnowledgeLabeledEvalAliases, MaxKnowledgeLabeledEvalCitations)
		}
		for i, alias := range claim.Aliases {
			if err := validateKnowledgeLabeledText("claim alias", alias); err != nil {
				return fmt.Errorf("knowledge labeled expected claim %q: %w", claim.ID, err)
			}
			if i > 0 && alias == claim.Aliases[i-1] {
				return fmt.Errorf("knowledge labeled expected claim %q has duplicate alias %q", claim.ID, alias)
			}
		}
		if claim.NodeID != "" {
			if err := validateKnowledgeID(claim.NodeID); err != nil {
				return fmt.Errorf("knowledge labeled expected claim %q node_id: %w", claim.ID, err)
			}
		}
		for i, citationID := range claim.AcceptedCitationIDs {
			if !citationIDPattern.MatchString(citationID) {
				return fmt.Errorf("knowledge labeled expected claim %q has invalid citation ID %q", claim.ID, citationID)
			}
			if i > 0 && citationID == claim.AcceptedCitationIDs[i-1] {
				return fmt.Errorf("knowledge labeled expected claim %q has duplicate citation ID %q", claim.ID, citationID)
			}
		}
	}
	pairs := make(map[string]bool, len(manifest.Contradictions))
	contradictionIDs := make(map[string]bool, len(manifest.Contradictions))
	for _, item := range manifest.Contradictions {
		if err := validateKnowledgeID(item.ID); err != nil {
			return fmt.Errorf("knowledge labeled expected contradiction ID: %w", err)
		}
		if contradictionIDs[item.ID] {
			return fmt.Errorf("knowledge labeled expected contradiction ID %q is duplicated", item.ID)
		}
		contradictionIDs[item.ID] = true
		if !claimIDs[item.LeftClaimID] || !claimIDs[item.RightClaimID] {
			return fmt.Errorf("knowledge labeled expected contradiction %q references unknown claims %q and %q", item.ID, item.LeftClaimID, item.RightClaimID)
		}
		if item.LeftClaimID == item.RightClaimID {
			return fmt.Errorf("knowledge labeled expected contradiction %q references the same claim twice", item.ID)
		}
		pair := canonicalKnowledgeLabeledPair(item.LeftClaimID, item.RightClaimID)
		if pairs[pair] {
			return fmt.Errorf("knowledge labeled expected contradiction pair %q is duplicated", pair)
		}
		pairs[pair] = true
	}
	if knowledgeLabeledRequirementCount(manifest.Requirements) == 0 {
		return errors.New("knowledge labeled eval manifest must define at least one requirement")
	}
	for name, value := range map[string]*float64{
		"min_claim_precision":         manifest.Requirements.MinClaimPrecision,
		"min_claim_recall":            manifest.Requirements.MinClaimRecall,
		"min_claim_f1":                manifest.Requirements.MinClaimF1,
		"min_contradiction_precision": manifest.Requirements.MinContradictionPrecision,
		"min_contradiction_recall":    manifest.Requirements.MinContradictionRecall,
		"min_contradiction_f1":        manifest.Requirements.MinContradictionF1,
	} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 100) {
			return fmt.Errorf("knowledge labeled eval requirement %s must be 0..100", name)
		}
	}
	for name, value := range map[string]*int{
		"max_false_positive_claims":         manifest.Requirements.MaxFalsePositiveClaims,
		"max_false_negative_claims":         manifest.Requirements.MaxFalseNegativeClaims,
		"max_false_positive_contradictions": manifest.Requirements.MaxFalsePositiveContradictions,
		"max_false_negative_contradictions": manifest.Requirements.MaxFalseNegativeContradictions,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("knowledge labeled eval requirement %s must be non-negative", name)
		}
	}
	return nil
}

func validateKnowledgeLabeledText(label, value string) error {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > MaxKnowledgeLabeledEvalClaimTextRunes {
		return fmt.Errorf("%s must contain 1..%d valid UTF-8 runes", label, MaxKnowledgeLabeledEvalClaimTextRunes)
	}
	return nil
}

func knowledgeLabeledRequirementCount(r KnowledgeLabeledEvalRequirements) int {
	count := 0
	for _, present := range []bool{
		r.MinClaimPrecision != nil, r.MinClaimRecall != nil, r.MinClaimF1 != nil,
		r.MinContradictionPrecision != nil, r.MinContradictionRecall != nil, r.MinContradictionF1 != nil,
		r.MaxFalsePositiveClaims != nil, r.MaxFalseNegativeClaims != nil,
		r.MaxFalsePositiveContradictions != nil, r.MaxFalseNegativeContradictions != nil,
	} {
		if present {
			count++
		}
	}
	return count
}

// EvaluateKnowledgeLabeledQuality compares the current graph with a human
// benchmark. It uses one read-only SQLite snapshot, never calls a model and
// never mutates the database.
func (s *Store) EvaluateKnowledgeLabeledQuality(manifest KnowledgeLabeledEvalManifest) (KnowledgeLabeledEvalReport, error) {
	manifest = normalizeKnowledgeLabeledEvalManifest(manifest)
	if err := validateKnowledgeLabeledEvalManifest(manifest); err != nil {
		return KnowledgeLabeledEvalReport{}, err
	}
	snapshot, err := s.buildKnowledgeLabeledEvalSnapshot(manifest)
	if err != nil {
		return KnowledgeLabeledEvalReport{}, err
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return KnowledgeLabeledEvalReport{}, fmt.Errorf("encode knowledge labeled eval manifest: %w", err)
	}
	claimMatches, falsePositiveClaims, falseNegativeClaims, predictedToExpected := matchKnowledgeLabeledClaims(manifest, snapshot.claims)
	contradictionMatches, falsePositiveContradictions, falseNegativeContradictions := matchKnowledgeLabeledContradictions(manifest, snapshot.contradictions, predictedToExpected)
	report := KnowledgeLabeledEvalReport{
		SchemaVersion: KnowledgeLabeledEvalSchemaVersion, Name: manifest.Name,
		ManifestDigest: prefixedSHA256(canonical), GraphDigest: snapshot.graphDigest,
		EvidenceStateDigest: snapshot.evidenceStateDigest, SnapshotDigest: snapshot.snapshotDigest,
		Scope: manifest.Scope, Matching: manifest.Matching,
		Claims:         knowledgeLabeledMetrics(len(manifest.Claims), len(snapshot.claims), len(claimMatches)),
		Contradictions: knowledgeLabeledMetrics(len(manifest.Contradictions), len(snapshot.contradictions), len(contradictionMatches)),
		ClaimMatches:   claimMatches, FalsePositiveClaims: falsePositiveClaims, FalseNegativeClaims: falseNegativeClaims,
		ContradictionMatches: contradictionMatches, FalsePositiveContradictions: falsePositiveContradictions,
		FalseNegativeContradictions:      falseNegativeContradictions,
		ExcludedNonCurrentClaims:         snapshot.excludedClaims,
		ExcludedNonCurrentContradictions: snapshot.excludedContradictions,
		Passed:                           true,
	}
	report.Checks = buildKnowledgeLabeledChecks(manifest.Requirements, report.Claims, report.Contradictions)
	for _, check := range report.Checks {
		if !check.Passed {
			report.Passed = false
		}
	}
	return report, nil
}

func (s *Store) buildKnowledgeLabeledEvalSnapshot(manifest KnowledgeLabeledEvalManifest) (knowledgeLabeledEvalSnapshot, error) {
	if s == nil || s.db == nil {
		return knowledgeLabeledEvalSnapshot{}, errors.New("knowledge labeled eval store is unavailable")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return knowledgeLabeledEvalSnapshot{}, fmt.Errorf("begin knowledge labeled eval snapshot: %w", err)
	}
	defer tx.Rollback()
	graph, err := loadKnowledgeGraphFromQuerier(tx)
	if err != nil {
		return knowledgeLabeledEvalSnapshot{}, err
	}
	if len(graph.Nodes) > MaxKnowledgeGraphExportNodes || len(graph.Edges) > MaxKnowledgeGraphExportEdges {
		return knowledgeLabeledEvalSnapshot{}, fmt.Errorf("knowledge labeled eval exceeds graph node/edge limits %d/%d", MaxKnowledgeGraphExportNodes, MaxKnowledgeGraphExportEdges)
	}
	selected, scopeDigest, err := loadKnowledgeLabeledEvalScope(tx, manifest.Scope)
	if err != nil {
		return knowledgeLabeledEvalSnapshot{}, err
	}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		return knowledgeLabeledEvalSnapshot{}, fmt.Errorf("encode knowledge labeled eval graph digest: %w", err)
	}
	graphDigest := prefixedSHA256(graphJSON)
	stateHash := sha256.New()
	writeKnowledgeIDField(stateHash, "knowledge-labeled-evidence-state-v1")
	writeEvidenceState := func(objectType KnowledgeObjectType, objectID string, anchors []EvidenceAnchor) error {
		writeKnowledgeIDField(stateHash, string(objectType))
		writeKnowledgeIDField(stateHash, objectID)
		for _, anchor := range anchors {
			resolution, resolveErr := resolveClassicMindMapEvidenceWithQuery(tx, anchor)
			if resolveErr != nil {
				return fmt.Errorf("resolve knowledge labeled eval evidence %s %q: %w", objectType, objectID, resolveErr)
			}
			writeKnowledgeIDField(stateHash, anchor.CitationID)
			writeKnowledgeIDField(stateHash, string(resolution.State))
			writeKnowledgeIDField(stateHash, resolution.CurrentDocumentRevision)
			writeKnowledgeIDField(stateHash, resolution.CurrentChunkHash)
		}
		return nil
	}
	for _, node := range graph.Nodes {
		if err := writeEvidenceState(KnowledgeObjectNode, node.ID, node.Evidence); err != nil {
			return knowledgeLabeledEvalSnapshot{}, err
		}
	}
	for _, edge := range graph.Edges {
		if err := writeEvidenceState(KnowledgeObjectEdge, edge.ID, edge.Evidence); err != nil {
			return knowledgeLabeledEvalSnapshot{}, err
		}
	}
	evidenceStateDigest := "sha256:" + hex.EncodeToString(stateHash.Sum(nil))
	statusAllowed := make(map[KnowledgeStatus]bool, len(manifest.Matching.Statuses))
	for _, status := range manifest.Matching.Statuses {
		statusAllowed[status] = true
	}
	snapshot := knowledgeLabeledEvalSnapshot{graphDigest: graphDigest, evidenceStateDigest: evidenceStateDigest}
	claimByID := make(map[string]KnowledgeLabeledClaimPrediction)
	for _, node := range graph.Nodes {
		if node.Kind != KnowledgeNodeClaim || !statusAllowed[node.Status] {
			continue
		}
		evidence := knowledgeLabeledCurrentScopeEvidence(node.Evidence, selected)
		if len(evidence) == 0 {
			snapshot.excludedClaims++
			continue
		}
		prediction := KnowledgeLabeledClaimPrediction{
			NodeID: node.ID, Label: node.Label,
			Body:   truncateRunes(node.Body, MaxKnowledgeLabeledEvalClaimTextRunes),
			Status: node.Status, Evidence: evidence,
		}
		snapshot.claims = append(snapshot.claims, prediction)
		claimByID[node.ID] = prediction
	}
	for _, edge := range graph.Edges {
		if edge.Kind != KnowledgeRelationContradicts || !statusAllowed[edge.Status] {
			continue
		}
		from, fromOK := claimByID[edge.From]
		to, toOK := claimByID[edge.To]
		if !fromOK || !toOK {
			snapshot.excludedContradictions++
			continue
		}
		evidence := knowledgeLabeledCurrentScopeEvidence(edge.Evidence, selected)
		if len(evidence) == 0 {
			snapshot.excludedContradictions++
			continue
		}
		snapshot.contradictions = append(snapshot.contradictions, KnowledgeLabeledContradictionPrediction{
			EdgeID: edge.ID, Label: edge.Label, Status: edge.Status,
			FromNodeID: edge.From, FromLabel: from.Label, ToNodeID: edge.To, ToLabel: to.Label, Evidence: evidence,
		})
	}
	sort.Slice(snapshot.claims, func(i, j int) bool { return snapshot.claims[i].NodeID < snapshot.claims[j].NodeID })
	sort.Slice(snapshot.contradictions, func(i, j int) bool { return snapshot.contradictions[i].EdgeID < snapshot.contradictions[j].EdgeID })
	matchingJSON, _ := json.Marshal(manifest.Matching)
	snapshot.snapshotDigest = prefixedSHA256([]byte(graphDigest + "\n" + evidenceStateDigest + "\n" + scopeDigest + "\n" + string(matchingJSON)))
	if err := tx.Commit(); err != nil {
		return knowledgeLabeledEvalSnapshot{}, fmt.Errorf("commit knowledge labeled eval read snapshot: %w", err)
	}
	return snapshot, nil
}

func loadKnowledgeLabeledEvalScope(tx *sql.Tx, scope KnowledgeCoverageOptions) (map[string][]Entry, string, error) {
	rows, err := tx.Query(`SELECT id, title, text, tags, source_file, chunk_label, chunk_index, total_chunks,
document_id, document_revision, chunk_hash, source_path, media_type, page, block_index,
block_marker, block_chunk_index, block_total_chunks FROM entries ORDER BY id`)
	if err != nil {
		return nil, "", fmt.Errorf("load knowledge labeled eval scope: %w", err)
	}
	defer rows.Close()
	selected := make(map[string][]Entry)
	documentMatched := scope.Document == ""
	for rows.Next() {
		var entry Entry
		var tagsJSON string
		if err := rows.Scan(&entry.ID, &entry.Title, &entry.Text, &tagsJSON, &entry.SourceFile,
			&entry.ChunkLabel, &entry.ChunkIndex, &entry.TotalChunks, &entry.DocumentID,
			&entry.DocumentRevision, &entry.ChunkHash, &entry.SourcePath, &entry.MediaType,
			&entry.Page, &entry.BlockIndex, &entry.BlockMarker, &entry.BlockChunkIndex,
			&entry.BlockTotalChunks); err != nil {
			return nil, "", fmt.Errorf("scan knowledge labeled eval scope: %w", err)
		}
		entry.Tags, err = tagsFromJSON(tagsJSON)
		if err != nil {
			return nil, "", fmt.Errorf("decode knowledge labeled eval tags for entry %d: %w", entry.ID, err)
		}
		if !coverageEntryMatchesDocument(entry, scope.Document) {
			continue
		}
		documentMatched = true
		if !coverageEntryHasTag(entry, scope.Tag) || !coverageEntryInPages(entry, scope.PageFrom, scope.PageTo) {
			continue
		}
		if strings.TrimSpace(entry.Text) == "" || entry.DocumentID == "" || entry.DocumentRevision == "" ||
			entry.ChunkHash == "" || entry.SourcePath == "" || entry.ChunkHash != ChunkContentHash(entry.Text) {
			continue
		}
		citationID, _ := CitationForEntry(entry)
		selected[citationID] = append(selected[citationID], entry)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate knowledge labeled eval scope: %w", err)
	}
	if !documentMatched {
		return nil, "", fmt.Errorf("knowledge labeled eval document %q was not found in current entries", scope.Document)
	}
	h := sha256.New()
	writeKnowledgeIDField(h, "knowledge-labeled-scope-v1")
	writeKnowledgeIDField(h, scope.Document)
	writeKnowledgeIDField(h, strings.ToLower(scope.Tag))
	writeKnowledgeIDField(h, fmt.Sprintf("%d", scope.PageFrom))
	writeKnowledgeIDField(h, fmt.Sprintf("%d", scope.PageTo))
	ids := make([]string, 0, len(selected))
	for citationID := range selected {
		ids = append(ids, citationID)
	}
	sort.Strings(ids)
	for _, citationID := range ids {
		entries := selected[citationID]
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].DocumentRevision != entries[j].DocumentRevision {
				return entries[i].DocumentRevision < entries[j].DocumentRevision
			}
			return entries[i].ChunkHash < entries[j].ChunkHash
		})
		selected[citationID] = entries
		for _, entry := range entries {
			writeKnowledgeIDField(h, citationID)
			writeKnowledgeIDField(h, entry.DocumentRevision)
			writeKnowledgeIDField(h, entry.ChunkHash)
		}
	}
	return selected, "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func knowledgeLabeledCurrentScopeEvidence(anchors []EvidenceAnchor, selected map[string][]Entry) []KnowledgeLabeledEvidenceRef {
	result := make([]KnowledgeLabeledEvidenceRef, 0, len(anchors))
	seen := make(map[string]bool)
	for _, anchor := range anchors {
		current := false
		for _, entry := range selected[anchor.CitationID] {
			if coverageAnchorMatchesEntry(anchor, entry) {
				current = true
				break
			}
		}
		if !current || seen[anchor.CitationID] {
			continue
		}
		seen[anchor.CitationID] = true
		result = append(result, KnowledgeLabeledEvidenceRef{
			CitationID: anchor.CitationID, SourcePath: anchor.SourcePath, Page: anchor.Page,
			BlockIndex: anchor.BlockIndex, BlockChunkIndex: anchor.BlockChunkIndex,
			DocumentRevision: anchor.DocumentRevision, ChunkHash: anchor.ChunkHash,
			Excerpt: truncateRunes(anchor.Excerpt, MaxKnowledgeLabeledEvalReportExcerptRunes),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CitationID < result[j].CitationID })
	return result
}

type knowledgeLabeledClaimCandidate struct {
	expected  int
	predicted int
	score     float64
}

func matchKnowledgeLabeledClaims(manifest KnowledgeLabeledEvalManifest, predictions []KnowledgeLabeledClaimPrediction) ([]KnowledgeLabeledClaimMatch, []KnowledgeLabeledClaimPrediction, []KnowledgeLabeledExpectedClaim, map[string]string) {
	threshold := *manifest.Matching.ClaimSimilarityThreshold
	adjacency := make([][]knowledgeLabeledClaimCandidate, len(manifest.Claims))
	for expectedIndex, expected := range manifest.Claims {
		for predictedIndex, prediction := range predictions {
			if expected.NodeID != "" && expected.NodeID != prediction.NodeID {
				continue
			}
			if !knowledgeLabeledCitationAccepted(expected.AcceptedCitationIDs, prediction.Evidence) {
				continue
			}
			score := knowledgeLabeledClaimSimilarity(expected, prediction)
			if score >= threshold {
				adjacency[expectedIndex] = append(adjacency[expectedIndex], knowledgeLabeledClaimCandidate{expected: expectedIndex, predicted: predictedIndex, score: score})
			}
		}
		sort.Slice(adjacency[expectedIndex], func(i, j int) bool {
			if adjacency[expectedIndex][i].score != adjacency[expectedIndex][j].score {
				return adjacency[expectedIndex][i].score > adjacency[expectedIndex][j].score
			}
			return predictions[adjacency[expectedIndex][i].predicted].NodeID < predictions[adjacency[expectedIndex][j].predicted].NodeID
		})
	}
	matchedPrediction := make([]int, len(predictions))
	for i := range matchedPrediction {
		matchedPrediction[i] = -1
	}
	var augment func(int, []bool) bool
	augment = func(expected int, seen []bool) bool {
		for _, candidate := range adjacency[expected] {
			if seen[candidate.predicted] {
				continue
			}
			seen[candidate.predicted] = true
			if matchedPrediction[candidate.predicted] == -1 || augment(matchedPrediction[candidate.predicted], seen) {
				matchedPrediction[candidate.predicted] = expected
				return true
			}
		}
		return false
	}
	for expected := range manifest.Claims {
		augment(expected, make([]bool, len(predictions)))
	}
	matchedExpected := make(map[int]int)
	for predicted, expected := range matchedPrediction {
		if expected >= 0 {
			matchedExpected[expected] = predicted
		}
	}
	matches := make([]KnowledgeLabeledClaimMatch, 0, len(matchedExpected))
	predictedToExpected := make(map[string]string, len(matchedExpected))
	for expected, predicted := range matchedExpected {
		score := 0.0
		for _, candidate := range adjacency[expected] {
			if candidate.predicted == predicted {
				score = candidate.score
				break
			}
		}
		matches = append(matches, KnowledgeLabeledClaimMatch{
			ExpectedID: manifest.Claims[expected].ID, NodeID: predictions[predicted].NodeID,
			Similarity: score, Prediction: predictions[predicted],
		})
		predictedToExpected[predictions[predicted].NodeID] = manifest.Claims[expected].ID
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ExpectedID < matches[j].ExpectedID })
	falsePositive := make([]KnowledgeLabeledClaimPrediction, 0)
	for i, prediction := range predictions {
		if matchedPrediction[i] < 0 {
			falsePositive = append(falsePositive, prediction)
		}
	}
	falseNegative := make([]KnowledgeLabeledExpectedClaim, 0)
	for i, expected := range manifest.Claims {
		if _, ok := matchedExpected[i]; !ok {
			falseNegative = append(falseNegative, expected)
		}
	}
	return matches, falsePositive, falseNegative, predictedToExpected
}

func knowledgeLabeledCitationAccepted(accepted []string, evidence []KnowledgeLabeledEvidenceRef) bool {
	if len(accepted) == 0 {
		return true
	}
	allowed := make(map[string]bool, len(accepted))
	for _, citationID := range accepted {
		allowed[citationID] = true
	}
	for _, item := range evidence {
		if allowed[item.CitationID] {
			return true
		}
	}
	return false
}

func knowledgeLabeledClaimSimilarity(expected KnowledgeLabeledExpectedClaim, prediction KnowledgeLabeledClaimPrediction) float64 {
	expectedTexts := append([]string{expected.Text}, expected.Aliases...)
	predictedTexts := []string{prediction.Label, prediction.Body, strings.TrimSpace(prediction.Label + " " + prediction.Body)}
	best := 0.0
	for _, left := range expectedTexts {
		for _, right := range predictedTexts {
			score := knowledgeLabeledTokenDice(left, right)
			if score > best {
				best = score
			}
		}
	}
	return best
}

func knowledgeLabeledTokenDice(left, right string) float64 {
	leftTokens, rightTokens := knowledgeLabeledTokens(left), knowledgeLabeledTokens(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}
	leftCounts := make(map[string]int, len(leftTokens))
	for _, token := range leftTokens {
		leftCounts[token]++
	}
	intersection := 0
	for _, token := range rightTokens {
		if leftCounts[token] > 0 {
			intersection++
			leftCounts[token]--
		}
	}
	return 2 * float64(intersection) / float64(len(leftTokens)+len(rightTokens))
}

func knowledgeLabeledTokens(value string) []string {
	var tokens []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			tokens = append(tokens, string(current))
			current = current[:0]
		}
	}
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

func matchKnowledgeLabeledContradictions(manifest KnowledgeLabeledEvalManifest, predictions []KnowledgeLabeledContradictionPrediction, predictedToExpected map[string]string) ([]KnowledgeLabeledContradictionMatch, []KnowledgeLabeledContradictionPrediction, []KnowledgeLabeledExpectedContradiction) {
	expectedByPair := make(map[string]int, len(manifest.Contradictions))
	for i, item := range manifest.Contradictions {
		expectedByPair[canonicalKnowledgeLabeledPair(item.LeftClaimID, item.RightClaimID)] = i
	}
	matchedExpected := make(map[int]bool)
	matches := make([]KnowledgeLabeledContradictionMatch, 0)
	falsePositive := make([]KnowledgeLabeledContradictionPrediction, 0)
	for _, prediction := range predictions {
		left, leftOK := predictedToExpected[prediction.FromNodeID]
		right, rightOK := predictedToExpected[prediction.ToNodeID]
		expectedIndex, ok := expectedByPair[canonicalKnowledgeLabeledPair(left, right)]
		if !leftOK || !rightOK || !ok || matchedExpected[expectedIndex] {
			falsePositive = append(falsePositive, prediction)
			continue
		}
		matchedExpected[expectedIndex] = true
		matches = append(matches, KnowledgeLabeledContradictionMatch{
			ExpectedID: manifest.Contradictions[expectedIndex].ID, EdgeID: prediction.EdgeID, Prediction: prediction,
		})
	}
	falseNegative := make([]KnowledgeLabeledExpectedContradiction, 0)
	for i, expected := range manifest.Contradictions {
		if !matchedExpected[i] {
			falseNegative = append(falseNegative, expected)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ExpectedID < matches[j].ExpectedID })
	return matches, falsePositive, falseNegative
}

func canonicalKnowledgeLabeledPair(left, right string) string {
	if left > right {
		left, right = right, left
	}
	return left + "\x00" + right
}

func knowledgeLabeledMetrics(expected, predicted, truePositive int) KnowledgeLabeledMetrics {
	falsePositive := predicted - truePositive
	falseNegative := expected - truePositive
	precision := 100.0
	if predicted > 0 {
		precision = float64(truePositive) * 100 / float64(predicted)
	}
	recall := 100.0
	if expected > 0 {
		recall = float64(truePositive) * 100 / float64(expected)
	}
	f1 := 0.0
	if precision+recall > 0 {
		f1 = 2 * precision * recall / (precision + recall)
	}
	return KnowledgeLabeledMetrics{
		Expected: expected, Predicted: predicted, TruePositive: truePositive,
		FalsePositive: falsePositive, FalseNegative: falseNegative,
		Precision: precision, Recall: recall, F1: f1,
	}
}

func buildKnowledgeLabeledChecks(r KnowledgeLabeledEvalRequirements, claims, contradictions KnowledgeLabeledMetrics) []KnowledgeQualityCheck {
	checks := make([]KnowledgeQualityCheck, 0, knowledgeLabeledRequirementCount(r))
	appendMin := func(metric string, expected *float64, actual float64) {
		if expected != nil {
			checks = append(checks, KnowledgeQualityCheck{Metric: metric, Operator: ">=", Expected: *expected, Actual: actual, Unit: "percent", Passed: actual >= *expected})
		}
	}
	appendMax := func(metric string, expected *int, actual int) {
		if expected != nil {
			checks = append(checks, KnowledgeQualityCheck{Metric: metric, Operator: "<=", Expected: float64(*expected), Actual: float64(actual), Unit: "count", Passed: actual <= *expected})
		}
	}
	appendMin("claim_precision", r.MinClaimPrecision, claims.Precision)
	appendMin("claim_recall", r.MinClaimRecall, claims.Recall)
	appendMin("claim_f1", r.MinClaimF1, claims.F1)
	appendMin("contradiction_precision", r.MinContradictionPrecision, contradictions.Precision)
	appendMin("contradiction_recall", r.MinContradictionRecall, contradictions.Recall)
	appendMin("contradiction_f1", r.MinContradictionF1, contradictions.F1)
	appendMax("false_positive_claims", r.MaxFalsePositiveClaims, claims.FalsePositive)
	appendMax("false_negative_claims", r.MaxFalseNegativeClaims, claims.FalseNegative)
	appendMax("false_positive_contradictions", r.MaxFalsePositiveContradictions, contradictions.FalsePositive)
	appendMax("false_negative_contradictions", r.MaxFalseNegativeContradictions, contradictions.FalseNegative)
	return checks
}
