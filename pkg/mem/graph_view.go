package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"
	"time"
)

const KnowledgeMapViewVersion = 1

type KnowledgeMapViewData struct {
	Version          int                                `json:"version"`
	GeneratedAt      string                             `json:"generated_at"`
	Graph            KnowledgeGraph                     `json:"graph"`
	Review           KnowledgeReviewReport              `json:"review"`
	LatestReviews    []KnowledgeReviewRecord            `json:"latest_reviews,omitempty"`
	LatestEdits      []KnowledgeEditRecord              `json:"latest_edits,omitempty"`
	Creations        []KnowledgeWorkspaceCreationRecord `json:"workspace_creations,omitempty"`
	SelectionReports []KnowledgeSelectionReportRecord   `json:"selection_reports,omitempty"`
	RevisionDiff     KnowledgeRevisionDiffReport        `json:"revision_diff"`
	Merges           []KnowledgeNodeMergeRecord         `json:"merges"`
	Layout           *KnowledgeMapLayout                `json:"layout,omitempty"`
	Workspace        *KnowledgeMapWorkspace             `json:"workspace,omitempty"`
	PortableExport   *KnowledgeGraphExportPin           `json:"portable_export,omitempty"`
}

// KnowledgeMapWorkspace contains the short-lived capability required by the
// live page to save only its own visual layout. It is never added to an
// exported standalone HTML file.
type KnowledgeMapWorkspace struct {
	APIBase         string                    `json:"api_base,omitempty"`
	ApplicationURL  string                    `json:"application_url,omitempty"`
	SessionToken    string                    `json:"session_token"`
	ViewName        string                    `json:"view_name"`
	Views           []KnowledgeMapViewSummary `json:"views"`
	SelectionAnswer bool                      `json:"selection_answer"`
}

func (s *Store) BuildKnowledgeMapViewData() (applogResult0 KnowledgeMapViewData, applogResult1 error) {
	defer applog.Track("pkg.mem.Store.BuildKnowledgeMapViewData")(&applogResult1)

	return s.BuildKnowledgeMapViewDataForView(DefaultKnowledgeMapView)
}

// BuildKnowledgeMapViewDataForView renders the graph with one explicitly
// selected visual view. Saved views change presentation only; graph objects,
// reviews, edits, and evidence remain shared and current.
func (s *Store) BuildKnowledgeMapViewDataForView(viewName string) (applogResult0 KnowledgeMapViewData, applogResult1 error) {
	defer applog.Track("pkg.mem.Store.BuildKnowledgeMapViewDataForView")(&applogResult1)

	viewName, err := validateKnowledgeMapViewName(viewName)
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	exportSnapshot, err := s.buildKnowledgeGraphSnapshot(true, true)
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	graph := exportSnapshot.Graph
	review, err := reviewKnowledgeGraph(graph, exportSnapshot.Resolved)
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	latestReviews, err := s.ListLatestKnowledgeReviews()
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	latestEdits, err := s.ListLatestKnowledgeEdits()
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	creations, err := s.ListKnowledgeWorkspaceCreations(10000)
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	selectionReports, err := s.ListKnowledgeSelectionReports(10000)
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	merges, err := s.ListKnowledgeNodeMerges(1000)
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	revisionDiff, err := s.buildKnowledgeRevisionDiff(KnowledgeRevisionDiffOptions{}, graph, review, exportSnapshot.CurrentEntries)
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	layout, err := s.LoadKnowledgeMapLayout(viewName)
	if err != nil {
		return KnowledgeMapViewData{}, err
	}
	return KnowledgeMapViewData{
		Version: KnowledgeMapViewVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Graph: graph, Review: review, LatestReviews: latestReviews, LatestEdits: latestEdits, Creations: creations, SelectionReports: selectionReports,
		RevisionDiff: revisionDiff, Merges: merges, Layout: layout, PortableExport: &exportSnapshot.Pin,
	}, nil
}

func WriteKnowledgeMapHTML(w io.Writer, title string, data KnowledgeMapViewData) (applogResult0 error) {
	defer applog.Track("pkg.mem.WriteKnowledgeMapHTML")(&applogResult0)

	if w == nil {
		return fmt.Errorf("knowledge map HTML writer is nil")
	}
	if data.Version != KnowledgeMapViewVersion {
		return fmt.Errorf("unsupported knowledge map view version %d", data.Version)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "mem-tool — Knowledge Map"
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode knowledge map data: %w", err)
	}
	if _, err := io.WriteString(w, strings.Replace(knowledgeMapHTMLPrefix, "{{TITLE}}", html.EscapeString(title), 1)); err != nil {
		return fmt.Errorf("write knowledge map HTML: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write knowledge map payload: %w", err)
	}
	if _, err := io.WriteString(w, strings.NewReplacer("/*__KNOWLEDGE_TOPICS__*/", knowledgeTopicsJS, "/*__KNOWLEDGE_TOPICS_VIEW__*/", knowledgeTopicsViewJS, "/*__KNOWLEDGE_SOURCE_PREVIEW__*/", knowledgeSourcePreviewJS).Replace(knowledgeMapHTMLSuffix)); err != nil {
		return fmt.Errorf("write knowledge map HTML: %w", err)
	}
	return nil
}

//go:embed web/knowledgeMapHTMLPrefix.html
var knowledgeMapHTMLPrefix string

//go:embed web/knowledgeMapHTMLSuffix.html
var knowledgeMapHTMLSuffix string

//go:embed web/knowledgeTopics.js
var knowledgeTopicsJS string

//go:embed web/knowledgeTopicsView.js
var knowledgeTopicsViewJS string

//go:embed web/knowledgeSourcePreview.js
var knowledgeSourcePreviewJS string
