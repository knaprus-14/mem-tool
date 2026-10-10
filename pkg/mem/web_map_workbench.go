package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrWebMapBranchInvalid = errors.New("выберите ветвь и актуальную ревизию карты; вопрос — до 8000 символов")
var ErrWebMapBranchEmpty = errors.New("в ветви нет актуальных фрагментов документов; прикрепите их через «Источники узла»")
var ErrWebMapBranchChanged = errors.New("ветвь или её источники изменились; заново откройте предпросмотр")

type WebMapBranchInput struct {
	Revision int64  `json:"revision"`
	Question string `json:"question,omitempty"`
	Manifest string `json:"manifest,omitempty"`
}

type WebMapBranchView struct {
	Manifest     ClassicMindMapBranchManifest `json:"manifest"`
	Study        ClassicMindMapStudyPack      `json:"study"`
	Unavailable  int                          `json:"unavailable_sources"`
	OtherSources int                          `json:"other_sources"`
}

// Branch reads the tree and evidence from one bounded SQL snapshot. No Store
// recovery, vector cache, filesystem locators or model calls are involved.
func ReadWebMapBranchIn(ctx context.Context, dir, mapID, nodeID string, revision int64) (applogResult0 WebMapBranchView, applogResult1 error) {
	defer applog.Track("pkg.mem.ReadWebMapBranchIn")(&applogResult1)

	return readWebMapBranchIn(ctx, dir, mapID, nodeID, revision, true)
}

func readWebMapBranchIn(ctx context.Context, dir, mapID, nodeID string, revision int64, redact bool) (WebMapBranchView, error) {
	var result WebMapBranchView
	if !webMapExactID(mapID) || !webMapExactID(nodeID) || revision <= 0 {
		return result, ErrWebMapBranchInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	result, err = readWebMapBranchTx(ctx, tx, mapID, nodeID, revision, redact)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func readWebMapBranchTx(ctx context.Context, tx *sql.Tx, mapID, nodeID string, revision int64, redact bool) (WebMapBranchView, error) {
	var result WebMapBranchView
	doc, err := readWebMapDocumentTx(ctx, tx, mapID)
	if err != nil {
		return result, err
	}
	// Web routes accept exact IDs, while the terminal also accepts labels.
	found := false
	for _, n := range doc.Nodes {
		if n.ID == nodeID {
			found = true
			break
		}
	}
	if !found {
		return result, ErrClassicMindMapNodeNotFound
	}
	doc, root, nodes, err := classicMindMapBranchFromDocument(doc, ClassicMindMapBranchRequest{MapRef: mapID, NodeRef: nodeID, ExpectedRevision: revision})
	if err != nil {
		return result, err
	}
	resolved, err := resolveWebMapBranchEvidenceTx(ctx, tx, nodes, redact)
	if err != nil {
		return result, err
	}
	result.Unavailable, result.OtherSources = resolved.Unavailable, resolved.Other
	doc.StateDigest, err = classicMindMapAIDigest(struct{ Map, Sources string }{doc.StateDigest, resolved.Digest})
	if err != nil {
		return result, err
	}
	result.Manifest, err = buildClassicMindMapBranchManifest(doc, root, nodes, resolved.Evidence)
	if err != nil {
		return result, err
	}
	for i := range nodes {
		for j := range nodes[i].Sources {
			redactWebMapSource(&nodes[i].Sources[j])
		}
	}
	result.Study = buildClassicMindMapStudyPack(doc, root, nodes, result.Manifest)
	for i := range result.Study.Cards {
		card := &result.Study.Cards[i]
		pin := resolved.Nodes[card.NodeID]
		card.EvidenceDigest = pin.Digest
		current := pin.Current
		card.EvidenceCurrent = &current
	}
	return result, nil
}

func redactWebMapSource(s *ClassicMindMapSource) {
	if s.Kind == ClassicMindMapSourceExternalFile {
		s.Title = "Локальный файл"
		s.Locator = ""
	}
	if s.Evidence != nil {
		copy := *s.Evidence
		copy.SourcePath = sourceName(Entry{SourcePath: copy.SourcePath})
		s.Evidence = &copy
		s.Locator = classicMindMapEvidenceLocator(copy)
		s.Title = classicMindMapEvidenceTitle(copy)
	}
}

func AnswerWebMapBranchIn(ctx context.Context, dir, mapID, nodeID string, in WebMapBranchInput) (applogResult0 ClassicMindMapBranchAnswer, applogResult1 error) {
	defer applog.Track("pkg.mem.AnswerWebMapBranchIn")(&applogResult1)

	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		return ClassicMindMapBranchAnswer{}, err
	}
	answer := cfg.Answer.WithDefaults()
	answer.MaxTokens = min(answer.MaxTokens, 4096)
	answer.ContextChars = min(answer.ContextChars, 48000)
	answer.TimeoutSeconds = min(answer.TimeoutSeconds, 90)
	provider, err := NewOllamaAnswerProvider(answer)
	if err != nil {
		return ClassicMindMapBranchAnswer{}, ErrQueryAnswer
	}
	return answerWebMapBranchWithService(ctx, dir, mapID, nodeID, in, &ClassicMindMapAIService{Provider: provider, Config: answer})
}

func answerWebMapBranchWithService(ctx context.Context, dir, mapID, nodeID string, in WebMapBranchInput, service *ClassicMindMapAIService) (ClassicMindMapBranchAnswer, error) {
	var result ClassicMindMapBranchAnswer
	in.Question = strings.TrimSpace(in.Question)
	if !utf8.ValidString(in.Question) || in.Question == "" || utf8.RuneCountInString(in.Question) > MaxClassicMindMapBranchQuestion || len(in.Manifest) != 64 || strings.Trim(in.Manifest, "0123456789abcdef") != "" {
		return result, ErrWebMapBranchInvalid
	}
	view, err := ReadWebMapBranchIn(ctx, dir, mapID, nodeID, in.Revision)
	if err != nil {
		return result, err
	}
	if view.Manifest.Digest != in.Manifest {
		return result, ErrWebMapBranchChanged
	}
	if len(view.Manifest.Evidence) == 0 {
		return result, ErrWebMapBranchEmpty
	}
	if service == nil || service.Provider == nil {
		return result, ErrQueryAnswer
	}
	result, err = answerClassicMindMapBranchManifest(ctx, service, in.Question, in.Manifest, view.Manifest)
	if err != nil {
		return ClassicMindMapBranchAnswer{}, ErrQueryAnswer
	}
	// Do not present an answer as current if an import/edit finished during generation.
	current, err := ReadWebMapBranchIn(ctx, dir, mapID, nodeID, in.Revision)
	if err != nil {
		return ClassicMindMapBranchAnswer{}, err
	}
	if current.Manifest.Digest != in.Manifest {
		return ClassicMindMapBranchAnswer{}, ErrWebMapBranchChanged
	}
	return result, nil
}

type WebMapCompareInput struct {
	Revision      int64  `json:"revision"`
	OtherID       string `json:"other_id"`
	OtherRevision int64  `json:"other_revision"`
}

func CompareWebMapsIn(ctx context.Context, dir, mapID string, in WebMapCompareInput) (applogResult0 ClassicMindMapComparison, applogResult1 error) {
	defer applog.Track("pkg.mem.CompareWebMapsIn")(&applogResult1)

	var result ClassicMindMapComparison
	if !webMapExactID(mapID) || !webMapExactID(in.OtherID) || in.Revision <= 0 || in.OtherRevision <= 0 {
		return result, ErrWebMapBranchInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := openQueryDB(ctx, dir)
	if err != nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	left, err := readWebMapDocumentTx(ctx, tx, mapID)
	if err != nil {
		return result, err
	}
	right, err := readWebMapDocumentTx(ctx, tx, in.OtherID)
	if err != nil {
		return result, err
	}
	if left.Map.Revision != in.Revision || right.Map.Revision != in.OtherRevision {
		return result, ErrClassicMindMapRevisionConflict
	}
	result, err = compareClassicMindMapDocuments(ClassicMindMapCompareRequest{}, left, right)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
