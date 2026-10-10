package mem

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"context"
	"database/sql"
	"strings"
	"unicode/utf8"
)

type WebMapMetadata struct {
	Revision    int64                `json:"revision"`
	RequestID   string               `json:"request_id"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	Status      ClassicMindMapStatus `json:"status"`
}

func EditWebMapMetadataIn(ctx context.Context, dir, actor, mapID string, in WebMapMetadata) (applogResult0 WebMapNodeEdited, applogResult1 error) {
	defer applog.Track("pkg.mem.EditWebMapMetadataIn")(&applogResult1)

	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	if in.Title == "" || utf8.RuneCountInString(in.Title) > 512 || utf8.RuneCountInString(in.Description) > 4000 || !utf8.ValidString(in.Title+in.Description) || !validClassicMindMapStatus(in.Status) {
		return WebMapNodeEdited{}, ErrWebMapInvalid
	}
	return runWebMapMutation(ctx, dir, actor, mapID, in.Revision, in.RequestID, in, "edit_map", func(tx *sql.Tx, item ClassicMindMap) (string, error) {
		return editClassicMindMapTx(tx, item, ClassicMindMapPatch{Title: &in.Title, Description: &in.Description, Status: &in.Status})
	})
}

// ExportWebMapIn exports the full tree, regardless of browser folding, and never
// resolves local files. Hosted exports remove filesystem locators from metadata.
func ExportWebMapIn(ctx context.Context, dir, mapID string, format ClassicMindMapExportFormat, revision int64, local bool) (applogResult0 ClassicMindMapExportArtifact, applogResult1 error) {
	defer applog.Track("pkg.mem.ExportWebMapIn")(&applogResult1)

	if !validClassicMindMapExportFormat(format) || revision <= 0 {
		return ClassicMindMapExportArtifact{}, ErrWebMapInvalid
	}
	doc, err := readWebMapDocument(ctx, dir, mapID)
	if err != nil {
		return ClassicMindMapExportArtifact{}, err
	}
	if doc.Map.Revision != revision {
		return ClassicMindMapExportArtifact{}, ErrClassicMindMapRevisionConflict
	}
	if !local {
		states := make(map[string]EvidenceState)
		for i := range doc.Nodes {
			for j := range doc.Nodes[i].Sources {
				s := &doc.Nodes[i].Sources[j]
				states[s.ID] = s.EvidenceState
				if s.Kind == ClassicMindMapSourceExternalFile {
					s.Title = "Локальный файл"
					s.Locator = ""
				}
				if s.Evidence != nil {
					s.Evidence.SourcePath = sourceName(Entry{SourcePath: s.Evidence.SourcePath})
					s.Locator = classicMindMapEvidenceLocator(*s.Evidence)
					s.Title = classicMindMapEvidenceTitle(*s.Evidence)
				}
			}
		}
		doc, _, err = finalizeClassicMindMapDocument(doc)
		if err != nil {
			return ClassicMindMapExportArtifact{}, err
		}
		for i := range doc.Nodes {
			for j := range doc.Nodes[i].Sources {
				s := &doc.Nodes[i].Sources[j]
				s.EvidenceState = states[s.ID]
			}
		}
		doc.StateDigest = classicMindMapResolvedStateDigest(doc)
	}
	return renderClassicMindMapArtifact(doc, format)
}
