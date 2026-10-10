package mem

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Opt-in replay of a private, read-only corpus audit. No user database or model
// is opened. Fixtures stay outside the repository's tracked source tree.
func TestKnowledgeRequirementRealAuditReplay(t *testing.T) {
	path := os.Getenv("MEM_GRAPH_REQUIREMENT_AUDIT")
	if path == "" {
		t.Skip("set MEM_GRAPH_REQUIREMENT_AUDIT to a local audit JSON")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Checks []struct {
			Label            string `json:"label"`
			Body             string `json:"body"`
			CitationID       string `json:"citation_id"`
			DocumentRevision string `json:"document_revision"`
			ChunkHash        string `json:"chunk_hash"`
			Page             int    `json:"page"`
			Excerpt          string `json:"excerpt"`
			Required         string `json:"required_fragment"`
			Present          bool   `json:"present_in_body"`
		}
	}
	if err = json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit.Checks) == 0 {
		t.Fatal("audit is empty")
	}
	for _, c := range audit.Checks {
		t.Run(c.Label+"/"+c.Required, func(t *testing.T) {
			if !strings.Contains(normalizeKnowledgeIdentityText(c.Excerpt), normalizeKnowledgeIdentityText(c.Required)) {
				t.Fatal("required phrase is not grounded in source")
			}
			// Isolate the required-fragment gate from approximate lexical similarity.
			expected := KnowledgeLabeledExpectedClaim{Text: c.Body, RequiredFragments: []string{c.Required}}
			score := knowledgeLabeledClaimSimilarity(expected, KnowledgeLabeledClaimPrediction{Body: c.Body})
			if (score > 0) != c.Present {
				t.Fatalf("qualifier gate disagrees with recorded audit: %v", score)
			}
			t.Logf("physical page %d, qualifier present=%v", c.Page, c.Present)
		})
	}
}
