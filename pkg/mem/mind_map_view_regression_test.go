package mem

import (
	"strings"
	"testing"
)

func TestClassicMindMapWorkspaceUsesBoundedLinearLookups(t *testing.T) {
	for _, fragment := range []string{
		"nodeIndex=new Map(doc.nodes.map",
		"function nodeByID(id){return nodeIndex.get(id)}",
		"buttons=new Map",
		"readCollapsed(doc.map.id)",
		"writeLocalStorage('mem-mindmap-collapsed-'",
		"function syncCurrentMapSummary(){if(!doc)return",
		"syncCurrentMapSummary();nodeIndex=new Map",
		"current.revision=doc.map.revision",
		"current.source_count=doc.nodes.reduce",
	} {
		if !strings.Contains(classicMindMapWorkspaceHTML, fragment) {
			t.Errorf("workspace is missing regression guard %q", fragment)
		}
	}
	if strings.Contains(classicMindMapWorkspaceHTML, "subtreeHasMatch") {
		t.Error("workspace restored recursive subtree search")
	}
}

func TestClassicMindMapWorkspaceWrapsIntermediateHeader(t *testing.T) {
	for _, fragment := range []string{
		"@media(max-width:1200px) and (min-width:901px)",
		".editor-head{height:auto;min-height:58px;flex-wrap:wrap;padding:9px}",
		".search{width:180px}",
	} {
		if !strings.Contains(classicMindMapWorkspaceHTML, fragment) {
			t.Errorf("workspace is missing intermediate-width regression guard %q", fragment)
		}
	}
}
