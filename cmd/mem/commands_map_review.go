package main

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func handleMapExportHTML(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapExportHTML")(&applogResult0)

	if len(args) == 0 {
		return errors.New("использование: mem map export-html <output.html> [--title <текст>] [--force]")
	}
	outputPath := strings.TrimSpace(args[0])
	if outputPath == "" || strings.HasPrefix(outputPath, "-") {
		return errors.New("map export-html: укажи путь к output.html первым аргументом")
	}
	title, force := "", false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--force":
			force = true
		case "--title":
			if i+1 >= len(args) {
				return errors.New("использование: mem map export-html <output.html> [--title <текст>] [--force]")
			}
			i++
			title = args[i]
		default:
			return fmt.Errorf("неизвестный аргумент map export-html: %s", args[i])
		}
	}
	if !strings.EqualFold(filepath.Ext(outputPath), ".html") && !strings.EqualFold(filepath.Ext(outputPath), ".htm") {
		return errors.New("map export-html: выходной файл должен иметь расширение .html или .htm")
	}
	if info, err := os.Stat(outputPath); err == nil {
		if info.IsDir() {
			return fmt.Errorf("map export-html: %s является папкой", outputPath)
		}
		if !force {
			return fmt.Errorf("map export-html: файл %s уже существует; используй --force для перезаписи", outputPath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("map export-html: inspect output: %w", err)
	}
	data, err := store.BuildKnowledgeMapViewData()
	if err != nil {
		return fmt.Errorf("map export-html: %w", err)
	}
	var output bytes.Buffer
	if err := mem.WriteKnowledgeMapHTML(&output, title, data); err != nil {
		return fmt.Errorf("map export-html: %w", err)
	}
	if err := os.WriteFile(outputPath, output.Bytes(), 0o600); err != nil {
		return fmt.Errorf("map export-html: write output: %w", err)
	}
	absolute, err := filepath.Abs(outputPath)
	if err != nil {
		absolute = outputPath
	}
	fmt.Fprintf(os.Stdout, "Интерактивная карта сохранена: %s (nodes=%d edges=%d merges=%d)\n",
		absolute, len(data.Graph.Nodes), len(data.Graph.Edges), len(data.Merges))
	return nil
}

func handleMapStatus(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapStatus")(&applogResult0)

	jsonOutput := false
	if len(args) > 1 || (len(args) == 1 && args[0] != "--json") {
		return errors.New("использование: mem map status [--json]")
	}
	if len(args) == 1 {
		jsonOutput = true
	}
	report, err := store.ReviewKnowledgeGraph()
	if err != nil {
		return fmt.Errorf("map status: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("map status: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Карта: total=%d draft=%d active=%d rejected=%d resolved=%d ready=%d\n",
		report.Summary.Total, report.Summary.Draft, report.Summary.Active, report.Summary.Rejected,
		report.Summary.Resolved, report.Summary.Ready)
	fmt.Fprintf(os.Stdout, "Evidence: current=%d stale=%d missing=%d\n",
		report.Summary.CurrentEvidence, report.Summary.StaleEvidence, report.Summary.MissingEvidence)
	for _, item := range report.Items {
		ready := ""
		if item.ReadyForApproval {
			ready = " ready"
		}
		label := strings.Join(strings.Fields(item.Label), " ")
		fmt.Fprintf(os.Stdout, "- %s %s [%s/%s evidence=%s%s] %s\n",
			item.ObjectType, item.ID, item.Kind, item.Status, item.EvidenceState, ready, label)
		fmt.Fprintf(os.Stdout, "  evidence_digest %s\n  review_digest %s\n", item.EvidenceDigest, item.ReviewDigest)
		for _, evidence := range item.Evidence {
			fmt.Fprintf(os.Stdout, "  %s %s\n", evidence.State, evidence.Anchor.CitationID)
		}
	}
	return nil
}

func handleMapApprove(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapApprove")(&applogResult0)

	if len(args) < 4 {
		return errors.New("использование: mem map approve <node|edge> <id> --reviewer <имя> [--comment <текст>] [--evidence-digest <sha256>] [--review-digest <sha256>]")
	}
	objectType := mem.KnowledgeObjectType(args[0])
	if objectType != mem.KnowledgeObjectNode && objectType != mem.KnowledgeObjectEdge {
		return errors.New("тип объекта должен быть node или edge")
	}
	request := mem.KnowledgeApprovalRequest{ObjectType: objectType, ID: args[1]}
	for i := 2; i < len(args); i++ {
		if i+1 >= len(args) {
			return fmt.Errorf("для %s требуется значение", args[i])
		}
		value := args[i+1]
		switch args[i] {
		case "--reviewer":
			request.Reviewer = value
		case "--comment":
			request.Comment = value
		case "--evidence-digest":
			request.ExpectedEvidenceDigest = value
		case "--review-digest":
			if strings.TrimSpace(value) == "" {
				return errors.New("--review-digest требует непустой sha256 из mem map status --json")
			}
			request.ExpectedReviewDigest = value
		default:
			return fmt.Errorf("неизвестный флаг map approve: %s", args[i])
		}
		i++
	}
	approved, err := store.ApproveCurrentKnowledgeObjectWithReview(request)
	if err != nil {
		return fmt.Errorf("map approve: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Подтверждено: %s %s %s->%s evidence=%d review=%d digest=%s\n",
		approved.ObjectType, approved.ID, approved.PreviousStatus, approved.Status,
		len(approved.Evidence), approved.Review.ID, approved.EvidenceDigest)
	return nil
}

func handleMapApproveBatch(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapApproveBatch")(&applogResult0)

	if len(args) != 1 {
		return errors.New("использование: mem map approve-batch <manifest.json>")
	}
	file, err := os.Open(args[0])
	if err != nil {
		return fmt.Errorf("map approve-batch: read manifest: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return fmt.Errorf("map approve-batch: read manifest: %w", err)
	}
	if len(data) > 1<<20 {
		return errors.New("map approve-batch: manifest exceeds 1 MiB")
	}
	var manifest mem.KnowledgeApprovalManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("map approve-batch: decode manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("map approve-batch: decode manifest: %w", err)
	}
	requests := make([]mem.KnowledgeApprovalRequest, 0, len(manifest.Objects))
	for _, object := range manifest.Objects {
		comment := object.Comment
		if comment == "" {
			comment = manifest.Comment
		}
		requests = append(requests, mem.KnowledgeApprovalRequest{
			ObjectType: object.ObjectType, ID: object.ID, Reviewer: manifest.Reviewer,
			Comment: comment, ExpectedEvidenceDigest: object.ExpectedEvidenceDigest, ExpectedReviewDigest: object.ExpectedReviewDigest,
		})
	}
	result, err := store.ApproveKnowledgeObjects(requests)
	if err != nil {
		return fmt.Errorf("map approve-batch: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Пакет подтверждён: objects=%d reviewer=%s\n", len(result.Approved), strings.TrimSpace(manifest.Reviewer))
	for _, approved := range result.Approved {
		fmt.Fprintf(os.Stdout, "- %s %s review=%d digest=%s\n", approved.ObjectType, approved.ID, approved.Review.ID, approved.EvidenceDigest)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("expected one JSON object")
		}
		return err
	}
	return nil
}

func handleMapReviews(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapReviews")(&applogResult0)

	jsonOutput, limit := false, 100
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-limit":
			if i+1 >= len(args) {
				return errors.New("использование: mem map reviews [--json] [-limit N]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > 1000 {
				return errors.New("map reviews: -limit должен быть от 1 до 1000")
			}
			limit = parsed
		default:
			return fmt.Errorf("неизвестный аргумент map reviews: %s", args[i])
		}
	}
	reviews, err := store.ListKnowledgeReviews(limit)
	if err != nil {
		return fmt.Errorf("map reviews: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(reviews, "", "  ")
		if err != nil {
			return fmt.Errorf("map reviews: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Review history: records=%d limit=%d\n", len(reviews), limit)
	for _, review := range reviews {
		fmt.Fprintf(os.Stdout, "- #%d %s %s %s->%s reviewer=%s created=%s\n",
			review.ID, review.ObjectType, review.ObjectID, review.PreviousStatus,
			review.NewStatus, review.Reviewer, review.Created)
		if review.Comment != "" {
			fmt.Fprintf(os.Stdout, "  comment %s\n", strings.Join(strings.Fields(review.Comment), " "))
		}
		fmt.Fprintf(os.Stdout, "  digest %s\n", review.EvidenceDigest)
	}
	return nil
}

func handleMapEdits(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapEdits")(&applogResult0)

	jsonOutput, limit := false, 100
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-limit":
			if i+1 >= len(args) {
				return errors.New("использование: mem map edits [--json] [-limit N]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > 1000 {
				return errors.New("map edits: -limit должен быть от 1 до 1000")
			}
			limit = parsed
		default:
			return fmt.Errorf("неизвестный аргумент map edits: %s", args[i])
		}
	}
	edits, err := store.ListKnowledgeEdits(limit)
	if err != nil {
		return fmt.Errorf("map edits: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(edits, "", "  ")
		if err != nil {
			return fmt.Errorf("map edits: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "История правок: записей=%d лимит=%d\n", len(edits), limit)
	for _, edit := range edits {
		action := "изменено"
		if edit.Action == mem.KnowledgeEditActionUndo {
			action = "правка отменена"
		}
		fmt.Fprintf(os.Stdout, "- %s %s · %s · %s · %s · %s->%s\n",
			edit.ObjectType, strings.Join(strings.Fields(edit.NewLabel), " "), action,
			strings.Join(strings.Fields(edit.Editor), " "), edit.Created, edit.PreviousStatus, edit.NewStatus)
		if edit.Comment != "" {
			fmt.Fprintf(os.Stdout, "  комментарий: %s\n", strings.Join(strings.Fields(edit.Comment), " "))
		}
	}
	return nil
}

func handleMapDuplicates(cfg *Config, store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapDuplicates")(&applogResult0)

	jsonOutput := false
	threshold := mem.DefaultKnowledgeDuplicateThreshold
	limit, nodeLimit := 100, 200
	var kind mem.KnowledgeNodeKind
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-threshold":
			if i+1 >= len(args) {
				return errors.New("использование: mem map duplicates [--json] [-threshold 0.92] [-kind claim] [-nodes N] [-limit N]")
			}
			i++
			parsed, err := strconv.ParseFloat(args[i], 64)
			if err != nil || parsed < 0 || parsed > 1 {
				return errors.New("map duplicates: -threshold должен быть от 0 до 1")
			}
			threshold = parsed
		case "-limit", "-nodes":
			flag := args[i]
			if i+1 >= len(args) {
				return errors.New("использование: mem map duplicates [--json] [-threshold 0.92] [-kind claim] [-nodes N] [-limit N]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > 1000 {
				return fmt.Errorf("map duplicates: %s должен быть от 1 до 1000", flag)
			}
			if flag == "-limit" {
				limit = parsed
			} else {
				nodeLimit = parsed
			}
		case "-kind":
			if i+1 >= len(args) {
				return errors.New("использование: mem map duplicates [--json] [-threshold 0.92] [-kind claim] [-nodes N] [-limit N]")
			}
			i++
			kind = mem.KnowledgeNodeKind(strings.TrimSpace(args[i]))
			if !cliKnowledgeNodeKind(kind) {
				return fmt.Errorf("map duplicates: неподдерживаемый kind %q", kind)
			}
		default:
			return fmt.Errorf("неизвестный аргумент map duplicates: %s", args[i])
		}
	}
	graph, err := store.LoadKnowledgeGraph()
	if err != nil {
		return fmt.Errorf("map duplicates: %w", err)
	}
	type pendingNode struct {
		node   mem.KnowledgeNode
		digest string
	}
	pending := make([]pendingNode, 0)
	for _, node := range graph.Nodes {
		if node.Status == mem.KnowledgeStatusResolved || (kind != "" && node.Kind != kind) {
			continue
		}
		current := true
		for _, anchor := range node.Evidence {
			if store.ResolveEvidenceAnchor(anchor).State != mem.EvidenceCurrent {
				current = false
				break
			}
		}
		if !current {
			continue
		}
		digest, err := mem.KnowledgeNodeContentDigest(node)
		if err != nil {
			return fmt.Errorf("map duplicates: digest node %s: %w", node.ID, err)
		}
		pending = append(pending, pendingNode{node: node, digest: digest})
		if len(pending) == nodeLimit {
			break
		}
	}
	identity, err := mem.EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return fmt.Errorf("map duplicates: %w", err)
	}
	vectors := make([]mem.KnowledgeNodeVector, 0, len(pending))
	if len(pending) >= 2 {
		rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := mem.AnswerContext(rootCtx, cfg.Answer.WithDefaults())
		defer cancel()
		for i, item := range pending {
			text := strings.TrimSpace(item.node.Label)
			if body := strings.TrimSpace(item.node.Body); body != "" {
				text += "\n\n" + body
			}
			fmt.Fprintf(os.Stderr, "[MAP DUPLICATES] embedding node=%d/%d id=%s\n", i+1, len(pending), item.node.ID)
			vector, err := getEmbeddingContext(ctx, cfg, text)
			if err != nil {
				return fmt.Errorf("map duplicates: embed node %s: %w", item.node.ID, err)
			}
			vectors = append(vectors, mem.KnowledgeNodeVector{NodeID: item.node.ID, NodeDigest: item.digest, Embedding: vector})
		}
	}
	report, err := store.DetectKnowledgeNodeDuplicates(vectors, identity.SpaceID, threshold, limit, kind)
	if err != nil {
		return fmt.Errorf("map duplicates: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("map duplicates: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Duplicate candidates: pairs=%d threshold=%.4f eligible=%d scanned=%d embedded=%d\n",
		len(report.Candidates), report.Threshold, report.EligibleNodes, report.ScannedNodes, len(vectors))
	fmt.Fprintf(os.Stdout, "Skipped: resolved=%d non_current=%d changed=%d no_vector=%d\n",
		report.SkippedResolved, report.SkippedNonCurrent, report.SkippedChanged, report.SkippedNoVector)
	for _, candidate := range report.Candidates {
		fmt.Fprintf(os.Stdout, "- similarity=%.6f kind=%s %s <> %s\n",
			candidate.Similarity, candidate.Left.Kind, candidate.Left.ID, candidate.Right.ID)
		fmt.Fprintf(os.Stdout, "  %s <> %s\n", candidate.Left.Label, candidate.Right.Label)
		if candidate.SuggestedSource != "" {
			fmt.Fprintf(os.Stdout, "  merge suggestion: source=%s target=%s\n", candidate.SuggestedSource, candidate.SuggestedTarget)
		}
		fmt.Fprintf(os.Stdout, "  left_node=%s left_evidence=%s\n", candidate.Left.NodeDigest, candidate.Left.EvidenceDigest)
		fmt.Fprintf(os.Stdout, "  right_node=%s right_evidence=%s\n", candidate.Right.NodeDigest, candidate.Right.EvidenceDigest)
	}
	return nil
}

func cliKnowledgeNodeKind(kind mem.KnowledgeNodeKind) bool {
	switch kind {
	case mem.KnowledgeNodeDocument, mem.KnowledgeNodeSection, mem.KnowledgeNodeTopic,
		mem.KnowledgeNodeDefinition, mem.KnowledgeNodeClaim, mem.KnowledgeNodeFormula,
		mem.KnowledgeNodeExample, mem.KnowledgeNodeProcedure, mem.KnowledgeNodeEvent, mem.KnowledgeNodeComparison,
		mem.KnowledgeNodeNote, mem.KnowledgeNodeQuestion, mem.KnowledgeNodeCard,
		mem.KnowledgeNodeHypothesis, mem.KnowledgeNodeDecision, mem.KnowledgeNodeTask,
		mem.KnowledgeNodeContradiction, mem.KnowledgeNodeGap, mem.KnowledgeNodeDependency,
		mem.KnowledgeNodeCause, mem.KnowledgeNodeEffect, mem.KnowledgeNodeRisk,
		mem.KnowledgeNodeConstraint:
		return true
	default:
		return false
	}
}

func handleMapMergeNode(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapMergeNode")(&applogResult0)

	if len(args) != 1 {
		return errors.New("использование: mem map merge-node <manifest.json>")
	}
	file, err := os.Open(args[0])
	if err != nil {
		return fmt.Errorf("map merge-node: open manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var request mem.KnowledgeNodeMergeRequest
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("map merge-node: decode manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("map merge-node: decode manifest: %w", err)
	}
	result, err := store.MergeKnowledgeDuplicate(request)
	if err != nil {
		return fmt.Errorf("map merge-node: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Duplicate node merged: source=%s target=%s kind=%s similarity=%.6f resolved_edges=%d merge=%d review=%d\n",
		result.Merge.SourceID, result.Merge.TargetID, result.Merge.Kind, result.Merge.Similarity,
		result.ResolvedEdges, result.Merge.ID, result.Review.ID)
	return nil
}

func handleMapMerges(store *Store, args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleMapMerges")(&applogResult0)

	jsonOutput, limit := false, 100
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "-limit":
			if i+1 >= len(args) {
				return errors.New("использование: mem map merges [--json] [-limit N]")
			}
			i++
			parsed, err := strconv.Atoi(args[i])
			if err != nil || parsed < 1 || parsed > 1000 {
				return errors.New("map merges: -limit должен быть от 1 до 1000")
			}
			limit = parsed
		default:
			return fmt.Errorf("неизвестный аргумент map merges: %s", args[i])
		}
	}
	records, err := store.ListKnowledgeNodeMerges(limit)
	if err != nil {
		return fmt.Errorf("map merges: %w", err)
	}
	if jsonOutput {
		encoded, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			return fmt.Errorf("map merges: encode report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintf(os.Stdout, "Node merge history: records=%d limit=%d\n", len(records), limit)
	for _, record := range records {
		state := "stale"
		if record.Current {
			state = "current"
		}
		fmt.Fprintf(os.Stdout, "- #%d [%s] %s -> %s kind=%s similarity=%.6f reviewer=%s created=%s\n",
			record.ID, state, record.SourceID, record.TargetID, record.Kind, record.Similarity, record.Reviewer, record.Created)
		if record.StateReason != "" {
			fmt.Fprintf(os.Stdout, "  reason %s\n", record.StateReason)
		}
		if record.Comment != "" {
			fmt.Fprintf(os.Stdout, "  comment %s\n", strings.Join(strings.Fields(record.Comment), " "))
		}
	}
	return nil
}
