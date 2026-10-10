package mem

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Review pins include semantic structure; the existing editing content_digest
// intentionally remains the digest of label/body for compatibility.
type knowledgeReviewContent struct {
	Version                         string
	Type                            KnowledgeObjectType
	ID, Kind, Label, Body, From, To string
	Origin                          KnowledgeOrigin
}

func knowledgeReviewDigest(content knowledgeReviewContent) string {
	content.Version = "knowledge-review-content-v1"
	data, _ := json.Marshal(content)
	return prefixedSHA256(data)
}

type knowledgeReviewQuerier interface {
	QueryRow(string, ...any) *sql.Row
}

func loadKnowledgeReviewDigest(q knowledgeReviewQuerier, objectType KnowledgeObjectType, id string) (string, error) {
	content := knowledgeReviewContent{Type: objectType, ID: id}
	var err error
	switch objectType {
	case KnowledgeObjectNode:
		err = q.QueryRow(`SELECT kind,label,body,origin FROM knowledge_nodes WHERE id=?`, id).Scan(&content.Kind, &content.Label, &content.Body, &content.Origin)
	case KnowledgeObjectEdge:
		err = q.QueryRow(`SELECT kind,label,from_node,to_node,origin FROM knowledge_edges WHERE id=?`, id).Scan(&content.Kind, &content.Label, &content.From, &content.To, &content.Origin)
	default:
		return "", fmt.Errorf("unsupported knowledge object type %q", objectType)
	}
	if err != nil {
		return "", err
	}
	return knowledgeReviewDigest(content), nil
}

func requireKnowledgeReviewDigest(q knowledgeReviewQuerier, objectType KnowledgeObjectType, id, expected string) (string, error) {
	digest, err := loadKnowledgeReviewDigest(q, objectType, id)
	if err != nil {
		return "", err
	}
	if digest != expected {
		return "", fmt.Errorf("%w: reviewed content or relation structure changed", ErrKnowledgeContentChanged)
	}
	return digest, nil
}

var ErrKnowledgeReviewPinRequired = errors.New("expected_review_digest is required; refresh the map or obtain map status --json and review the object again")

// Add audit pins lazily in the same write transaction. Existing web databases
// can be read without migrations; historical append-only records stay unchanged.
const knowledgeReviewPinsSchema = `CREATE TABLE IF NOT EXISTS knowledge_review_pins (
 review_id INTEGER PRIMARY KEY REFERENCES knowledge_reviews(id), review_digest TEXT NOT NULL);
 CREATE TRIGGER IF NOT EXISTS knowledge_review_pins_no_update BEFORE UPDATE ON knowledge_review_pins
 BEGIN SELECT RAISE(ABORT,'knowledge review pins are append-only'); END;
 CREATE TRIGGER IF NOT EXISTS knowledge_review_pins_no_delete BEFORE DELETE ON knowledge_review_pins
 BEGIN SELECT RAISE(ABORT,'knowledge review pins are append-only'); END;`

func knowledgeReviewPinExpression(q knowledgeReviewQuerier) (string, error) {
	var exists int
	if err := q.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='knowledge_review_pins'`).Scan(&exists); err != nil {
		return "", err
	}
	if exists == 0 {
		return "''", nil
	}
	return `COALESCE((SELECT p.review_digest FROM knowledge_review_pins p WHERE p.review_id=r.id),'')`, nil
}
