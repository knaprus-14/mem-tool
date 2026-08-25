package mem

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeMapHTMLContainsOfflineInteractiveProvenancePayload(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	attack := `</script><script>alert("x")</script>`
	graph := KnowledgeGraph{
		Nodes: []KnowledgeNode{
			{ID: "view-claim", Kind: KnowledgeNodeClaim, Label: attack, Body: "Pinned claim", Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
			{ID: "view-gap", Kind: KnowledgeNodeGap, Label: "Evidence gap", Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}},
		},
		Edges: []KnowledgeEdge{{
			ID: "view-edge", From: "view-claim", To: "view-gap", Kind: KnowledgeRelationRevealsGap,
			Status: KnowledgeStatusDraft, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor},
		}},
	}
	if err := store.UpsertKnowledgeGraph(graph); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveKnowledgeMapLayout(DefaultKnowledgeMapView, KnowledgeMapLayout{
		Version: KnowledgeMapLayoutVersion,
		Nodes: map[string]KnowledgeMapNodePosition{
			"view-claim": {X: 120, Y: 80, Pinned: true},
		},
		Viewport: KnowledgeMapViewport{Scale: 1.2, X: 4, Y: -3},
		State: &KnowledgeMapViewState{
			Filters: KnowledgeMapViewFilters{
				Statuses:      []KnowledgeStatus{KnowledgeStatusActive, KnowledgeStatusDraft},
				Evidence:      []EvidenceState{EvidenceCurrent},
				NodeKinds:     []KnowledgeNodeKind{KnowledgeNodeClaim, KnowledgeNodeGap},
				RelationKinds: []KnowledgeRelationKind{KnowledgeRelationRevealsGap},
			},
			Focus:          &KnowledgeMapFocus{NodeID: "view-claim", Depth: 1},
			ClusterLayout:  true,
			Representation: KnowledgeMapRepresentationDocumentTree,
		},
	}); err != nil {
		t.Fatal(err)
	}
	data, err := store.BuildKnowledgeMapViewData()
	if err != nil {
		t.Fatal(err)
	}
	if data.Version != KnowledgeMapViewVersion || len(data.Graph.Nodes) != 2 || len(data.Review.Items) != 3 || data.Merges == nil || data.LatestEdits == nil || data.SelectionReports == nil ||
		data.RevisionDiff.Summary.Documents != 1 || data.RevisionDiff.Summary.CurrentAnchors != 3 ||
		data.Layout == nil || data.Layout.Nodes["view-claim"].X != 120 || data.Workspace != nil {
		t.Fatalf("view payload is incomplete: %#v", data)
	}
	var output bytes.Buffer
	if err := WriteKnowledgeMapHTML(&output, `Map </title><script>alert(1)</script>`, data); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, marker := range []string{
		`data-mem-map="v1"`, `Content-Security-Policy`, `function tick`, `pointerdown`,
		`statusFilters`, `evidenceFilters`, `relationFilters`, `showEvidence`,
		`contradiction`, `reveals_gap`, `document_revision`,
		`n._g.setPointerCapture(ev.pointerId)`, `if(ev.target!==svg)return`,
		`Технические данные источника`, `Внутренний ID`, `источник актуален`,
		`страница `, `фрагмент `, `refreshBtn`, `liveMode`,
		`resetLayoutBtn`, `scheduleLayoutSave`, `X-Mem-Session`, `savedLayout`,
		`Один щелчок открывает сведения`, `connect-src 'self'`,
		`sourceAction`, `ОТКРЫТЬ PDF`, `/api/source?citation=`,
		`Открытие физической страницы доступно через mem map open`,
		`reviewAction`, `ПОДТВЕРДИТЬ`, `/api/review/approve`,
		`ОТКЛОНИТЬ`, `ВЕРНУТЬ В РАБОТУ`, `ОТМЕНИТЬ ПОДТВЕРЖДЕНИЕ`,
		`/api/review/reject`, `/api/review/reopen`, `/api/review/undo`,
		`expected_evidence_digest`, `expected_review_id`, `Проверяющий`, `Комментарий / причина`,
		`rejected:'отклонено'`, `latest_reviews`,
		`editAction`, `РЕДАКТИРОВАНИЕ`, `СОХРАНИТЬ ПРАВКУ`, `ОТМЕНИТЬ ПОСЛЕДНЮЮ ПРАВКУ`,
		`/api/edit`, `/api/edit/undo`, `expected_content_digest`, `expected_edit_id`,
		`Автор правки`, `Комментарий к правке`, `latest_edits`,
		`clusterFilters`, `buildTopology`, `clusterCenter`, `cluster_layout`,
		`navigationAction`, `button.textContent='ФОКУС '+depth`, `СВЕРНУТЬ ВЕТВЬ`, `ПОКАЗАТЬ ВСЁ`,
		`saveViewBtn`, `viewSelect`, `layoutURL`, `version:9`, `mem_map_last_view`,
		`modalBackdrop`, `role="dialog"`, `showModal`, `Новое представление`,
		`workspaceCreateAction`, `РАБОЧИЙ СЛОЙ`, `СОЗДАТЬ И ПРИВЯЗАТЬ`,
		`/api/workspace/create`, `expected_parent_content_digest`, `workspace_creations`,
		`section`, `definition`, `formula`, `procedure`, `event`, `comparison`, `dependency`,
		`cause`, `effect`, `risk`, `constraint`, `depends_on`, `constrains`, `precedes`, `happens_before`,
		`hypothesis`, `decision`, `task`, `hypothesizes_about`, `based_on`, `acts_on`,
		`слой источника`, `аналитический слой`, `Уверенность извлечения`,
		`Покрытие источниками`, `version<3`, `version<4`, `filterCatalogValues`,
		`item.origin!=='manual'`,
		`themeBtn`, `mem_map_theme`, `data-theme="light"`, `setTheme`,
		`leftResizer`, `rightResizer`, `role="separator"`, `mem_map_panel_widths`,
		`bindPanelResizer`, `applyPanelWidths`, `--left-panel`, `--right-panel`,
		`ResizeObserver`, `aria-valuenow`, `dblclick`,
		`representationSelect`, `document-tree`, `buildDocumentTree`, `sourceName`,
		`updateDocumentTreeVisibility`, `nodeSearchText`, `ДЕРЕВО ДОКУМЕНТА`,
		`физической странице`, `state:layoutState()`,
		`ПРИЧИНЫ И СЛЕДСТВИЯ`, `causalView`, `causalGraph`, `causalRelationKinds`,
		`buildCausalDiagram`, `causalCurve`, `updateCausalVisibility`,
		`causes`, `mitigates`, `constrains`, `depends_on`,
		`ПОСЛЕДОВАТЕЛЬНОСТЬ`, `procedure-sequence`, `procedureView`,
		`buildProcedureSequences`, `procedureCoordinate`, `updateProcedureVisibility`,
		`procedure-transition`, `В текущем графе нет связей precedes`,
		`ХРОНОЛОГИЯ`, `timelineView`, `buildTimeline`, `updateTimelineVisibility`,
		`timeline-transition`, `События без установленного порядка`, `version<6`,
		`СРАВНЕНИЕ ДОКУМЕНТОВ`, `comparison-matrix`, `comparisonMatrix`,
		`buildComparisonMatrix`, `updateComparisonVisibility`, `comparison-cell`,
		`Нет evidence для документа`, `междокументный вывод неполон`,
		`ПРОТИВОРЕЧИЯ И ПРОБЕЛЫ`, `findings-board`, `findingsBoard`,
		`buildFindingsBoard`, `updateFindingsVisibility`, `finding-card`,
		`Draft — предложение модели`, `Междокументное evidence неполно`,
		`Связанные решения`, `mem map analyze`, `version<=9`,
		`selectionBtn`, `Ctrl+Click`, `currentSelectionRequest`, `selectionManifest`,
		`/api/selection/manifest`, `/api/selection/answer`, `expected_manifest_digest`,
		`СПРОСИТЬ ВЫБРАННОЕ`, `СДЕЛАТЬ СВОДКУ`, `только evidence выбранных объектов`,
		`/api/selection/explore`, `РАСШИРИТЬ ПО СВЯЗЯМ`, `НАЙТИ ПУТЬ`,
		`runSelectionExplore`, `applyExploredSelection`, `ВСЕ НАПРАВЛЕНИЯ`, `ГЛУБИНА `,
		`/api/selection/analyze`, `/api/selection/analyze/save`, `АНАЛИЗ ВЫБРАННОГО`,
		`СОХРАНИТЬ АНАЛИЗ В КАРТУ`, `runSelectionAnalysis`, `renderSelectionAnalysis`,
		`expected_analysis_digest`, `Нулевой счётчик не доказывает отсутствие свойства`,
		`selection_reports`, `selectionReportMeta`, `Автор сохранения`, `Сохранённый анализ`,
		`/api/selection/export`, `runSelectionExport`, `ОТЧЁТ · MARKDOWN`, `ПЛАН · MARKDOWN`,
		`ЧЕК-ЛИСТ · MARKDOWN`, `ТАБЛИЦА · CSV`, `ВЕТВЬ · JSON`, `без модели`,
		`/api/selection/learning/jobs/start`, `/api/selection/learning/jobs/status`,
		`/api/selection/learning/jobs/cancel`, `/api/selection/learning/jobs/result`,
		`learning-job-progress`, `selectionLearningJobID`, `/api/selection/learning/save`, `selectionLearningRun`,
		`СОЗДАТЬ УЧЕБНЫЕ КАНДИДАТЫ`, `СОХРАНИТЬ ВЫБРАННЫЕ КАК DRAFT`, `learning-preview`,
		`/api/selection/learning/route`, `runKnowledgeLearningRoute`, `ПОСТРОИТЬ УЧЕБНЫЙ МАРШРУТ`,
		`learning-route`, `НЕ ДОПУЩЕНО В МАРШРУТ`, `prerequisite/depends_on`,
		`/api/selection/learning/session/start`, `/api/selection/learning/session/grade`,
		`/api/selection/learning/history`, `startKnowledgeLearningSession`, `gradeKnowledgeLearningItem`,
		`/api/selection/learning/export`, `exportKnowledgeLearning`, `ЭКСПОРТ УЧЕБНЫХ МАТЕРИАЛОВ`,
		`ANKI · UTF-8 TSV`, `СПРАВОЧНИК · MARKDOWN`, `РАСПИСАНИЕ · CSV`,
		`НАЧАТЬ ПОВТОРЕНИЕ`, `ПОКАЗАТЬ ОТВЕТ`, `УЧЕБНЫЙ ПРОГРЕСС`, `learning-grade-grid`,
		`itemType=item.kind==='question'?'ВОПРОС':'КАРТОЧКА'`, `learningPlural(total,'элемент','элемента','элементов')`,
		`learningPlural(attempts,'попытка','попытки','попыток')`,
		`runKnowledgeLearningRoute(status,learningRoute,learningRouteBox,learningStart,learningHistory,learningSessionBox,learningHistoryBox,learningExportButton,learningExport)`,
		`renderObjectDetails('node',node);setDetailsOpen(true)`,
		`learningFocus`, `learningFocusContent`, `openKnowledgeLearningFocus`, `closeKnowledgeLearningFocus`,
		`Пробел — показать ответ`, `ПРОДОЛЖИТЬ СЕССИЮ`, `button.dataset.grade=grade`,
		`clusterSelectionBtn`, `ДОБАВИТЬ ВЕСЬ КЛАСТЕР`, `toggleActiveClusterSelection`,
		`sessionStorage.setItem(workingSelectionStorageKey`, `restoreWorkingSelection`, `maxWorkingSelectionNodes=200`,
		`data-workspace-mode="map"`, `data-workspace-mode="review"`, `data-workspace-mode="analysis"`,
		`data-workspace-mode="learning"`, `setWorkspaceMode`, `reviewWorkspace`, `selectionWorkbench`,
		`selectionTray`, `clearWorkingSelection`, `inspector-tabs`, `ДОБАВИТЬ В ВЫБРАННУЮ ОБЛАСТЬ`,
		`clusterSearch`, `clusterMoreBtn`, `filterClusterCatalog`, `nth-child(n+7)`,
		`selection.size&&selectionPanel.classList.contains('hidden')`,
		`.app:not([data-workspace="map"]) #representationSelect`,
		`class:'edge-group'`, `.edge-group.excluded,.edge-group.semantic-hidden,.edge-group.lens-hidden{display:none}`, `.edge-group.dim{opacity:.08}`,
		`updateGraphDensity`, `labels-compact`, `labels-hidden`, `updateGraphContext`,
		`semanticZoomSelect`, `relationLensSelect`, `contextTrail`, `semanticReadout`,
		`currentSemanticLevel`, `semanticNodeSet`, `relationEdgeSet`, `updateGraphPresentation`,
		`relation_lens:relationLens`, `semantic_zoom:semanticZoom`, `на экране '+shownNodes+' из '+visibleGraphNodes`,
		`связей '+visibleEdges+' из '+edges.length`,
		`arrangeBtn`, `arrangeClusterFocus`, `restoreClusterFocusLayout`, `clusterFocusSnapshot`,
		`clusterLaneHeaders`, `ТЕМА И ИСТОЧНИК`, `ОПОРНЫЕ ЗНАНИЯ`, `АНАЛИТИКА И РЕШЕНИЯ`,
		`const original=clusterFocusSnapshot.get(n.id)`, `const viewportState=clusterFocusViewport`,
		`data-workspace-mode="revisions"`, `revisionWorkspace`, `revision_diff`, `buildRevisionWorkspace`,
		`Изменения источников`, `БЫЛО В КАРТЕ`, `ТЕКУЩИЙ CHUNK`, `revisionLimitations`,
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("HTML is missing %q", marker)
		}
	}
	if strings.Contains(html, `details.append(title,badges,field('ID',item.id))`) ||
		strings.Contains(html, `details.append(field('Evidence digest'`) ||
		strings.Contains(html, `title.textContent=item.label||item.id`) ||
		strings.Contains(html, `<aside id="details" class="details"><section id="selectionPanel"`) {
		t.Fatal("technical identifiers are still rendered in the primary details view")
	}
	for _, unsupported := range []string{`prompt(`, `confirm(`, `alert('`} {
		if strings.Contains(html, unsupported) {
			t.Fatalf("map still depends on unsupported blocking browser dialog %q", unsupported)
		}
	}
	if strings.Contains(html, attack) || strings.Contains(html, `</title><script>alert(1)</script>`) {
		t.Fatal("untrusted title or graph text escaped its data/title context")
	}
	if !strings.Contains(html, `\u003c/script\u003e`) || !strings.Contains(html, `&lt;/title&gt;`) {
		t.Fatal("HTML does not contain safely escaped untrusted text")
	}
	if strings.Contains(html, `src="http`) || strings.Contains(html, `href="http`) {
		t.Fatal("offline map contains a remote dependency")
	}
	const payloadStart = `<script id="mem-map-data" type="application/json">`
	start := strings.Index(html, payloadStart)
	if start < 0 {
		t.Fatal("map payload start not found")
	}
	start += len(payloadStart)
	end := strings.Index(html[start:], `</script>`)
	if end < 0 {
		t.Fatal("map payload end not found")
	}
	var decoded KnowledgeMapViewData
	if err := json.Unmarshal([]byte(html[start:start+end]), &decoded); err != nil {
		t.Fatalf("embedded payload is not valid JSON: %v", err)
	}
	if len(decoded.Graph.Nodes) != 2 || decoded.Graph.Nodes[0].Label != attack || decoded.Review.Items[0].Evidence[0].Anchor.SourcePath == "" ||
		decoded.RevisionDiff.Summary.Documents != 1 || decoded.RevisionDiff.Summary.CurrentAnchors != 3 ||
		decoded.Layout == nil || decoded.Layout.Nodes["view-claim"].Pinned != true || decoded.Layout.State == nil ||
		decoded.Layout.State.Focus.NodeID != "view-claim" || !decoded.Layout.State.ClusterLayout ||
		decoded.Layout.State.Representation != KnowledgeMapRepresentationDocumentTree || decoded.Workspace != nil {
		t.Fatalf("embedded payload lost graph provenance: %#v", decoded)
	}
}

func TestKnowledgeMapHTMLRejectsInvalidArguments(t *testing.T) {
	if err := WriteKnowledgeMapHTML(nil, "", KnowledgeMapViewData{Version: KnowledgeMapViewVersion}); err == nil {
		t.Fatal("nil writer was accepted")
	}
	if err := WriteKnowledgeMapHTML(&bytes.Buffer{}, "", KnowledgeMapViewData{Version: KnowledgeMapViewVersion + 1}); err == nil {
		t.Fatal("unknown view version was accepted")
	}
}

func TestKnowledgeMapBrowserScriptHasValidJavaScriptSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	var output bytes.Buffer
	if err := WriteKnowledgeMapHTML(&output, "syntax", KnowledgeMapViewData{
		Version: KnowledgeMapViewVersion,
		Graph:   KnowledgeGraph{},
		Review:  KnowledgeReviewReport{},
	}); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	start := strings.LastIndex(html, "<script>")
	end := strings.LastIndex(html, "</script>")
	if start < 0 || end <= start {
		t.Fatal("browser script not found")
	}
	scriptPath := filepath.Join(t.TempDir(), "knowledge-map.js")
	if err := os.WriteFile(scriptPath, []byte(html[start+len("<script>"):end]), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(node, "--check", scriptPath)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("knowledge-map JavaScript syntax check failed: %v\n%s", err, result)
	}
}
