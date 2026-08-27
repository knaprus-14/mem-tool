package mem

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeMapHTMLKeepsResponsiveStageUsable(t *testing.T) {
	var output bytes.Buffer
	if err := WriteKnowledgeMapHTML(&output, "responsive", KnowledgeMapViewData{Version: KnowledgeMapViewVersion}); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, want := range []string{
		"@media(max-width:1050px){.app{--left-splitter-active:0px;--right-splitter-active:0px;grid-template-columns:var(--left-active) minmax(320px,1fr)",
		"@media(max-width:760px){.app{display:block}.filters{position:absolute",
		".stage{position:absolute;inset:56px 0 0}",
		"@media(max-width:520px){header{height:100px;display:grid",
		".stage{inset:100px 0 0}",
		"const narrowPanelMedia=window.matchMedia?.('(max-width:760px)')||null",
		"function setFiltersOpen(open){if(open&&isNarrowPanelViewport())applyDetailsOpen(false);applyFiltersOpen(open)}",
		"function setDetailsOpen(open){if(open&&isNarrowPanelViewport())applyFiltersOpen(false);applyDetailsOpen(open)}",
		"if(workspaceMode==='map'){if(isNarrowPanelViewport()){setFiltersOpen(false);setDetailsOpen(false)}else setFiltersOpen(true)}else setFiltersOpen(false)",
		"initializeResponsivePanels();initUIPreferences()",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered map is missing responsive guard %q", want)
		}
	}
}

func TestKnowledgeMapMobilePanelsStartClosedAndRemainExclusive(t *testing.T) {
	var output bytes.Buffer
	if err := WriteKnowledgeMapHTML(&output, "mobile panels", KnowledgeMapViewData{Version: KnowledgeMapViewVersion}); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	start := strings.Index(html, "function isNarrowPanelViewport()")
	end := -1
	if start >= 0 {
		end = strings.Index(html[start:], "for(const button of document.querySelectorAll('[data-workspace-mode]'))")
	}
	if start < 0 || end < 0 {
		t.Fatal("responsive panel helpers were not found")
	}
	helper := html[start : start+end]
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	script := `
function classList(){const values=new Set();return{toggle:(name,force)=>{if(force)values.add(name);else values.delete(name)},contains:name=>values.has(name)}}
function element(){return{classList:classList(),attributes:new Map(),setAttribute(name,value){this.attributes.set(name,value)}}}
const filters=element(),details=element(),app=element(),filtersBtn=element(),detailsBtn=element();
const elements={filters,details,filtersBtn,detailsBtn};
const $=id=>elements[id];
let mediaListener=null;
const narrowPanelMedia={matches:true,addEventListener:(name,listener)=>{if(name==='change')mediaListener=listener}};
` + helper + `
initializeResponsivePanels();
if(!filters.classList.contains('closed')||!details.classList.contains('closed'))throw new Error('fresh narrow view did not close both panels');
if(!app.classList.contains('filters-closed')||!app.classList.contains('details-closed'))throw new Error('fresh narrow app columns stayed open');
setFiltersOpen(true);
if(filters.classList.contains('closed')||!details.classList.contains('closed'))throw new Error('opening filters did not keep details closed');
setDetailsOpen(true);
if(details.classList.contains('closed')||!filters.classList.contains('closed'))throw new Error('opening details did not close filters');
if(filtersBtn.attributes.get('aria-expanded')!=='false'||detailsBtn.attributes.get('aria-expanded')!=='true')throw new Error('panel controls do not expose state');
narrowPanelMedia.matches=false;
setFiltersOpen(true);setDetailsOpen(true);
if(filters.classList.contains('closed')||details.classList.contains('closed'))throw new Error('desktop panels became mutually exclusive');
narrowPanelMedia.matches=true;mediaListener({matches:true});
if(!filters.classList.contains('closed')||!details.classList.contains('closed'))throw new Error('entering narrow viewport did not expose the map');
`
	scriptPath := filepath.Join(t.TempDir(), "mobile-panels.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := exec.Command(node, scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("mobile panel regression failed: %v\n%s", err, result)
	}
}

func TestKnowledgeMapHTMLStopsHeavyLayoutAfterSettling(t *testing.T) {
	var output bytes.Buffer
	if err := WriteKnowledgeMapHTML(&output, "layout", KnowledgeMapViewData{Version: KnowledgeMapViewVersion}); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, want := range []string{
		"clusterByID.get(a._cluster)",
		"settleFrames>=45||simulationFrames>=900",
		"if(!document.hidden&&(renderDirty||moved)){render()",
		"performance.now()-simulationStartedAt>=4000",
		"role:'button','aria-label':edgeName",
		"role:'button','aria-label':(n.label||n.id)",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered map is missing layout/accessibility guard %q", want)
		}
	}
}

func TestKnowledgeMapHTMLRestoresDialogFocusToCollapsedMenuOpener(t *testing.T) {
	var output bytes.Buffer
	if err := WriteKnowledgeMapHTML(&output, "modal focus", KnowledgeMapViewData{Version: KnowledgeMapViewVersion}); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, want := range []string{
		"function captureDialogReturnFocus()",
		"disclosure:target.closest?.('details')||null",
		"if(state.disclosure&&!state.disclosure.open)state.disclosure.open=true",
		"previous=captureDialogReturnFocus()",
		"backdrop._returnFocus=captureDialogReturnFocus()",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered map is missing dialog focus guard %q", want)
		}
	}
	if got := strings.Count(html, "restoreDialogFocus(previous)"); got != 2 {
		t.Fatalf("dialog focus helper is used %d times, want both modal flows", got)
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	start := strings.Index(html, "function captureDialogReturnFocus()")
	end := strings.Index(html[start:], "function trapDialogTab(")
	if start < 0 || end < 0 {
		t.Fatal("dialog focus helper source not found")
	}
	helper := html[start : start+end]
	script := `
const disclosure={open:true};
const document={body:{},activeElement:null};
let frame=null;
const requestAnimationFrame=callback=>{frame=callback};
const opener={isConnected:true,closest:selector=>selector==='details'?disclosure:null,focus:()=>{document.activeElement=opener}};
document.activeElement=opener;
` + helper + `
const saved=captureDialogReturnFocus();
disclosure.open=false;
document.activeElement=document.body;
restoreDialogFocus(saved);
if(typeof frame!=='function')throw new Error('focus restoration was not scheduled');
frame();
if(!disclosure.open)throw new Error('the opener disclosure stayed closed');
if(document.activeElement!==opener)throw new Error('focus did not return to the opener');
`
	scriptPath := filepath.Join(t.TempDir(), "dialog-focus.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := exec.Command(node, scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("dialog focus regression failed: %v\n%s", err, result)
	}
}

func TestKnowledgeMapHTMLKeepsCanvasToolsClearOfSelectionTray(t *testing.T) {
	var output bytes.Buffer
	if err := WriteKnowledgeMapHTML(&output, "selection-tray", KnowledgeMapViewData{Version: KnowledgeMapViewVersion}); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, want := range []string{
		".app.has-selection .canvas-tools{bottom:72px}",
		"app.classList.toggle('has-selection',entries.length>0)",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered map is missing selection-tray collision guard %q", want)
		}
	}
}
