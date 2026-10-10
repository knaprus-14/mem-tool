package mem

import (
	"context"
	"database/sql"
	"time"
)

type classicMindMapResolvedBranch struct {
	Document ClassicMindMapDocument
	Root     ClassicMindMapNode
	Nodes    []ClassicMindMapNode
	Resolved webMapResolvedEvidence
}

// Keep map contents, linked graph objects and their exact document excerpts in
// one read snapshot. Terminal callers retain their legacy map/node references
// and local source paths, while sharing evidence semantics with web callers.
func (s *Store) loadResolvedClassicMindMapBranch(request ClassicMindMapBranchRequest) (classicMindMapResolvedBranch, error) {
	var result classicMindMapResolvedBranch
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	item, err := resolveClassicMindMapRef(tx, request.MapRef)
	if err != nil {
		return result, err
	}
	doc, err := loadClassicMindMapDocument(tx, item.ID)
	if err != nil {
		return result, err
	}
	doc, _, err = finalizeClassicMindMapDocument(doc)
	if err != nil {
		return result, err
	}
	doc, err = resolveClassicMindMapSourceStatesWithQuery(tx, doc)
	if err != nil {
		return result, err
	}
	// ExpectedStateDigest historically pins LoadClassicMindMap's document
	// state. Validate it before adding the deeper branch dependency digest.
	result.Document, result.Root, result.Nodes, err = classicMindMapBranchFromDocument(doc, request)
	if err != nil {
		return result, err
	}
	result.Resolved, err = resolveWebMapBranchEvidenceTx(ctx, tx, result.Nodes, false)
	if err != nil {
		return result, err
	}
	result.Document.StateDigest, err = classicMindMapAIDigest(struct{ Map, Sources string }{doc.StateDigest, result.Resolved.Digest})
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
