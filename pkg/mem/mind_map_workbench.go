package mem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ClassicMindMapWorkbenchVersion  = 1
	MaxClassicMindMapBranchQuestion = 8000
	MaxClassicMindMapCompareDetails = 10000
	MaxClassicMindMapStudyCards     = 10000
)

// ClassicMindMapTemplate is a built-in, deterministic starting structure. It
// contains no generated claims and never invokes a model.
type ClassicMindMapTemplate struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Branches    []string `json:"branches"`
}

type ClassicMindMapTemplateRequest struct {
	TemplateID  string `json:"template_id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

var classicMindMapTemplates = []ClassicMindMapTemplate{
	{ID: "brainstorm", Title: "Мозговой штурм", Description: "Цель, идеи, ограничения, вопросы и следующие шаги.", Branches: []string{"Цель", "Идеи", "Ограничения", "Открытые вопросы", "Следующие шаги"}},
	{ID: "study", Title: "Изучение темы", Description: "Понятия, факты, примеры, вопросы и источники.", Branches: []string{"Ключевые понятия", "Факты и определения", "Примеры", "Вопросы для проверки", "Источники"}},
	{ID: "decision", Title: "Принятие решения", Description: "Проблема, критерии, варианты, риски и решение.", Branches: []string{"Проблема", "Критерии", "Варианты", "Риски", "Решение"}},
	{ID: "project", Title: "План проекта", Description: "Цель, результаты, этапы, риски и действия.", Branches: []string{"Цель", "Ожидаемые результаты", "Этапы", "Риски", "Следующие действия"}},
	{ID: "document", Title: "Разбор документа", Description: "Структура полного разбора одного документа.", Branches: []string{"Краткое содержание", "Структура документа", "Ключевые положения", "Выводы", "Вопросы"}},
}

func ListClassicMindMapTemplates() []ClassicMindMapTemplate {
	result := make([]ClassicMindMapTemplate, len(classicMindMapTemplates))
	for i, item := range classicMindMapTemplates {
		result[i] = item
		result[i].Branches = append([]string(nil), item.Branches...)
	}
	return result
}

func (s *Store) CreateClassicMindMapFromTemplate(request ClassicMindMapTemplateRequest, actor string) (ClassicMindMapDocument, error) {
	templateID := strings.ToLower(strings.TrimSpace(request.TemplateID))
	var selected *ClassicMindMapTemplate
	for i := range classicMindMapTemplates {
		if classicMindMapTemplates[i].ID == templateID {
			selected = &classicMindMapTemplates[i]
			break
		}
	}
	if selected == nil {
		return ClassicMindMapDocument{}, fmt.Errorf("неизвестный шаблон классической карты %q", request.TemplateID)
	}
	title := strings.TrimSpace(request.Title)
	if title == "" {
		title = selected.Title
	}
	description := strings.TrimSpace(request.Description)
	if description == "" {
		description = selected.Description
	}
	nodes := []ClassicMindMapNodeDraft{{Ref: "root", Label: title, Kind: ClassicMindMapNodeTopic, Origin: ClassicMindMapNodeManual}}
	for i, label := range selected.Branches {
		nodes = append(nodes, ClassicMindMapNodeDraft{
			Ref: fmt.Sprintf("branch-%02d", i+1), ParentRef: "root", Label: label,
			Kind: ClassicMindMapNodeSubtopic, Origin: ClassicMindMapNodeManual,
		})
	}
	return s.ImportClassicMindMap(ClassicMindMapDraft{
		Title: title, Description: description, Mode: ClassicMindMapModeManual,
		Status: ClassicMindMapStatusDraft, Nodes: nodes,
	}, normalizeClassicMindMapActor(actor), "создано из встроенного шаблона "+selected.ID)
}

type ClassicMindMapBranchRequest struct {
	MapRef              string `json:"map_ref"`
	NodeRef             string `json:"node_ref"`
	ExpectedRevision    int64  `json:"expected_revision,omitempty"`
	ExpectedDigest      string `json:"expected_digest,omitempty"`
	ExpectedStateDigest string `json:"expected_state_digest,omitempty"`
}

type ClassicMindMapBranchManifest struct {
	Version     int                `json:"version"`
	MapID       string             `json:"map_id"`
	MapTitle    string             `json:"map_title"`
	RootNodeID  string             `json:"root_node_id"`
	RootLabel   string             `json:"root_label"`
	Revision    int64              `json:"revision"`
	MapDigest   string             `json:"map_digest"`
	StateDigest string             `json:"state_digest"`
	NodeIDs     []string           `json:"node_ids"`
	Evidence    []GroundedEvidence `json:"evidence"`
	Digest      string             `json:"digest"`
}

type ClassicMindMapBranchQuestionRequest struct {
	ClassicMindMapBranchRequest
	Question               string `json:"question"`
	ExpectedManifestDigest string `json:"expected_manifest_digest,omitempty"`
}

type ClassicMindMapBranchAnswer struct {
	Question       string             `json:"question"`
	Answer         string             `json:"answer"`
	Insufficient   bool               `json:"insufficient"`
	MapID          string             `json:"map_id"`
	RootNodeID     string             `json:"root_node_id"`
	ManifestDigest string             `json:"manifest_digest"`
	NodeCount      int                `json:"node_count"`
	EvidenceCount  int                `json:"evidence_count"`
	Sources        []GroundedEvidence `json:"sources"`
}

// BuildClassicMindMapBranchManifest pins the exact subtree and all current
// local evidence used by branch Q&A and study artifacts.
func (s *Store) BuildClassicMindMapBranchManifest(request ClassicMindMapBranchRequest) (ClassicMindMapBranchManifest, error) {
	doc, root, nodes, err := s.loadClassicMindMapBranch(request)
	if err != nil {
		return ClassicMindMapBranchManifest{}, err
	}
	evidence, err := s.currentClassicMindMapBranchEvidence(nodes)
	if err != nil {
		return ClassicMindMapBranchManifest{}, err
	}
	nodeIDs := make([]string, len(nodes))
	for i := range nodes {
		nodeIDs[i] = nodes[i].ID
	}
	payload := struct {
		Version     int                `json:"version"`
		MapID       string             `json:"map_id"`
		Revision    int64              `json:"revision"`
		MapDigest   string             `json:"map_digest"`
		StateDigest string             `json:"state_digest"`
		RootNodeID  string             `json:"root_node_id"`
		NodeIDs     []string           `json:"node_ids"`
		Evidence    []GroundedEvidence `json:"evidence"`
	}{ClassicMindMapWorkbenchVersion, doc.Map.ID, doc.Map.Revision, doc.Digest, doc.StateDigest, root.ID, nodeIDs, evidence}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ClassicMindMapBranchManifest{}, err
	}
	digest := sha256.Sum256(encoded)
	return ClassicMindMapBranchManifest{
		Version: ClassicMindMapWorkbenchVersion, MapID: doc.Map.ID, MapTitle: doc.Map.Title,
		RootNodeID: root.ID, RootLabel: root.Label, Revision: doc.Map.Revision,
		MapDigest: doc.Digest, StateDigest: doc.StateDigest, NodeIDs: nodeIDs,
		Evidence: evidence, Digest: hex.EncodeToString(digest[:]),
	}, nil
}

func (s *Store) AnswerClassicMindMapBranch(ctx context.Context, service *ClassicMindMapAIService, request ClassicMindMapBranchQuestionRequest) (ClassicMindMapBranchAnswer, error) {
	if service == nil || service.Provider == nil {
		return ClassicMindMapBranchAnswer{}, errors.New("answer-модель для вопроса по ветви недоступна")
	}
	question := strings.TrimSpace(request.Question)
	if question == "" {
		return ClassicMindMapBranchAnswer{}, errors.New("вопрос по ветви не может быть пустым")
	}
	if utf8.RuneCountInString(question) > MaxClassicMindMapBranchQuestion {
		return ClassicMindMapBranchAnswer{}, fmt.Errorf("вопрос по ветви превышает %d символов", MaxClassicMindMapBranchQuestion)
	}
	manifest, err := s.BuildClassicMindMapBranchManifest(request.ClassicMindMapBranchRequest)
	if err != nil {
		return ClassicMindMapBranchAnswer{}, err
	}
	if strings.TrimSpace(request.ExpectedManifestDigest) != "" && request.ExpectedManifestDigest != manifest.Digest {
		return ClassicMindMapBranchAnswer{}, fmt.Errorf("ветвь изменилась после предпросмотра: expected manifest %s, current %s", request.ExpectedManifestDigest, manifest.Digest)
	}
	if len(manifest.Evidence) == 0 {
		return ClassicMindMapBranchAnswer{}, errors.New("в выбранной ветви нет current versioned evidence; сначала привяжите источники")
	}
	contextQuestion := fmt.Sprintf("Ветка карты: %q. Вопрос пользователя: %s", manifest.RootLabel, question)
	cfg := service.Config.WithDefaults()
	prompt, err := buildGroundedPromptFromEvidence(contextQuestion, manifest.Evidence, cfg.ContextChars)
	if err != nil {
		return ClassicMindMapBranchAnswer{}, err
	}
	answerRequest := AnswerRequest{Model: cfg.Model, System: prompt.System, Prompt: prompt.User, MaxTokens: cfg.MaxTokens, Temperature: cfg.Temperature}
	schemaMode := UsesGroundedAnswerSchema(cfg.Model)
	if schemaMode {
		answerRequest, err = GroundedAnswerSchemaRequest(answerRequest, prompt.Evidence)
		if err != nil {
			return ClassicMindMapBranchAnswer{}, err
		}
	}
	generationContext, cancel := AnswerContext(ctx, cfg)
	defer cancel()
	raw, err := service.Provider.Generate(generationContext, answerRequest)
	if err != nil {
		return ClassicMindMapBranchAnswer{}, err
	}
	validated := ValidateGroundedAnswer(raw, prompt.Evidence)
	if schemaMode {
		validated = ValidateGroundedSchemaAnswer(raw, prompt.Evidence)
	} else if ShouldRetryGroundedAnswerWithSchema(raw, validated) {
		retry, schemaErr := GroundedAnswerSchemaRequest(answerRequest, prompt.Evidence)
		if schemaErr != nil {
			return ClassicMindMapBranchAnswer{}, schemaErr
		}
		raw, err = service.Provider.Generate(generationContext, retry)
		if err != nil {
			return ClassicMindMapBranchAnswer{}, err
		}
		validated = ValidateGroundedSchemaAnswer(raw, prompt.Evidence)
	}
	if validated.Rejected {
		return ClassicMindMapBranchAnswer{}, fmt.Errorf("grounded ответ по ветви отклонён: %s", validated.Reason)
	}
	return ClassicMindMapBranchAnswer{Question: question, Answer: validated.Answer, Insufficient: validated.Insufficient,
		MapID: manifest.MapID, RootNodeID: manifest.RootNodeID, ManifestDigest: manifest.Digest,
		NodeCount: len(manifest.NodeIDs), EvidenceCount: len(manifest.Evidence), Sources: validated.Used}, nil
}

type ClassicMindMapStudyCard struct {
	ID      string                 `json:"id"`
	NodeID  string                 `json:"node_id"`
	Kind    ClassicMindMapNodeKind `json:"kind"`
	Front   string                 `json:"front"`
	Back    string                 `json:"back"`
	Sources []ClassicMindMapSource `json:"sources"`
}

type ClassicMindMapStudyPack struct {
	Version        int                       `json:"version"`
	MapID          string                    `json:"map_id"`
	MapTitle       string                    `json:"map_title"`
	RootNodeID     string                    `json:"root_node_id"`
	RootLabel      string                    `json:"root_label"`
	Revision       int64                     `json:"revision"`
	ManifestDigest string                    `json:"manifest_digest"`
	Cards          []ClassicMindMapStudyCard `json:"cards"`
	Markdown       string                    `json:"markdown"`
}

func (s *Store) BuildClassicMindMapStudyPack(request ClassicMindMapBranchRequest) (ClassicMindMapStudyPack, error) {
	doc, root, nodes, err := s.loadClassicMindMapBranch(request)
	if err != nil {
		return ClassicMindMapStudyPack{}, err
	}
	manifest, err := s.BuildClassicMindMapBranchManifest(request)
	if err != nil {
		return ClassicMindMapStudyPack{}, err
	}
	children := make(map[string][]ClassicMindMapNode)
	for _, node := range nodes {
		children[node.ParentID] = append(children[node.ParentID], node)
	}
	cards := make([]ClassicMindMapStudyCard, 0, len(nodes))
	for _, node := range nodes {
		back := strings.TrimSpace(strings.Join(nonEmptyStrings(node.Summary, node.BodyMarkdown), "\n\n"))
		if back == "" && len(children[node.ID]) > 0 {
			labels := make([]string, len(children[node.ID]))
			for i := range children[node.ID] {
				labels[i] = children[node.ID][i].Label
			}
			back = strings.Join(labels, "; ")
		}
		if back == "" {
			continue
		}
		front := node.Label
		if node.Kind != ClassicMindMapNodeQuestion {
			front = fmt.Sprintf("Что важно знать о «%s»?", node.Label)
		}
		cards = append(cards, ClassicMindMapStudyCard{ID: "mmc-" + shortClassicMindMapDigest(node.ID+"\x00"+front+"\x00"+back), NodeID: node.ID, Kind: node.Kind, Front: front, Back: back, Sources: append([]ClassicMindMapSource(nil), node.Sources...)})
		if len(cards) >= MaxClassicMindMapStudyCards {
			break
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Учебный набор: %s\n\n", root.Label)
	fmt.Fprintf(&out, "> Карта: %s · ревизия %d · manifest `%s`\n\n", doc.Map.Title, doc.Map.Revision, manifest.Digest)
	for i, card := range cards {
		fmt.Fprintf(&out, "## %d. %s\n\n%s\n\n", i+1, card.Front, card.Back)
		writeClassicMindMapMarkdownSources(&out, card.Sources)
	}
	return ClassicMindMapStudyPack{Version: ClassicMindMapWorkbenchVersion, MapID: doc.Map.ID, MapTitle: doc.Map.Title,
		RootNodeID: root.ID, RootLabel: root.Label, Revision: doc.Map.Revision, ManifestDigest: manifest.Digest,
		Cards: cards, Markdown: out.String()}, nil
}

type ClassicMindMapCompareRequest struct {
	LeftMapRef          string `json:"left_map_ref"`
	RightMapRef         string `json:"right_map_ref"`
	LeftExpectedDigest  string `json:"left_expected_digest,omitempty"`
	RightExpectedDigest string `json:"right_expected_digest,omitempty"`
}

type ClassicMindMapCompareItem struct {
	Status    string   `json:"status"`
	Path      string   `json:"path"`
	OtherPath string   `json:"other_path,omitempty"`
	LeftID    string   `json:"left_id,omitempty"`
	RightID   string   `json:"right_id,omitempty"`
	Label     string   `json:"label"`
	Changes   []string `json:"changes,omitempty"`
}

type ClassicMindMapComparison struct {
	Version   int                         `json:"version"`
	Left      ClassicMindMapSummary       `json:"left"`
	Right     ClassicMindMapSummary       `json:"right"`
	Added     int                         `json:"added"`
	Removed   int                         `json:"removed"`
	Changed   int                         `json:"changed"`
	Moved     int                         `json:"moved"`
	Unchanged int                         `json:"unchanged"`
	Items     []ClassicMindMapCompareItem `json:"items"`
	Digest    string                      `json:"digest"`
}

func (s *Store) CompareClassicMindMaps(request ClassicMindMapCompareRequest) (ClassicMindMapComparison, error) {
	left, err := s.LoadClassicMindMap(request.LeftMapRef)
	if err != nil {
		return ClassicMindMapComparison{}, err
	}
	right, err := s.LoadClassicMindMap(request.RightMapRef)
	if err != nil {
		return ClassicMindMapComparison{}, err
	}
	if request.LeftExpectedDigest != "" && request.LeftExpectedDigest != left.Digest {
		return ClassicMindMapComparison{}, fmt.Errorf("левая карта изменилась после выбора")
	}
	if request.RightExpectedDigest != "" && request.RightExpectedDigest != right.Digest {
		return ClassicMindMapComparison{}, fmt.Errorf("правая карта изменилась после выбора")
	}
	leftNodes := classicMindMapComparableNodes(left)
	rightNodes := classicMindMapComparableNodes(right)
	items := make([]ClassicMindMapCompareItem, 0, len(leftNodes)+len(rightNodes))
	usedRight := make(map[string]bool)
	leftPaths := sortedClassicMindMapComparePaths(leftNodes)
	rightPaths := sortedClassicMindMapComparePaths(rightNodes)
	for _, path := range leftPaths {
		l := leftNodes[path]
		if r, ok := rightNodes[path]; ok {
			usedRight[path] = true
			changes := classicMindMapNodeChanges(l.Node, r.Node)
			status := "unchanged"
			if len(changes) > 0 {
				status = "changed"
			}
			items = append(items, ClassicMindMapCompareItem{Status: status, Path: path, LeftID: l.Node.ID, RightID: r.Node.ID, Label: l.Node.Label, Changes: changes})
			continue
		}
		movedPath := ""
		for _, candidatePath := range rightPaths {
			r := rightNodes[candidatePath]
			if usedRight[candidatePath] || candidatePath == path {
				continue
			}
			if l.Signature == r.Signature {
				movedPath = candidatePath
				break
			}
		}
		if movedPath != "" {
			usedRight[movedPath] = true
			r := rightNodes[movedPath]
			items = append(items, ClassicMindMapCompareItem{Status: "moved", Path: path, OtherPath: movedPath, LeftID: l.Node.ID, RightID: r.Node.ID, Label: l.Node.Label})
		} else {
			items = append(items, ClassicMindMapCompareItem{Status: "removed", Path: path, LeftID: l.Node.ID, Label: l.Node.Label})
		}
	}
	for _, path := range rightPaths {
		r := rightNodes[path]
		if usedRight[path] {
			continue
		}
		if _, exact := leftNodes[path]; exact {
			continue
		}
		items = append(items, ClassicMindMapCompareItem{Status: "added", Path: path, RightID: r.Node.ID, Label: r.Node.Label})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Status != items[j].Status {
			return items[i].Status < items[j].Status
		}
		return items[i].Path < items[j].Path
	})
	if len(items) > MaxClassicMindMapCompareDetails {
		return ClassicMindMapComparison{}, fmt.Errorf("сравнение превышает предел %d элементов", MaxClassicMindMapCompareDetails)
	}
	result := ClassicMindMapComparison{Version: ClassicMindMapWorkbenchVersion,
		Left: classicMindMapDocumentSummary(left), Right: classicMindMapDocumentSummary(right), Items: items}
	for _, item := range items {
		switch item.Status {
		case "added":
			result.Added++
		case "removed":
			result.Removed++
		case "changed":
			result.Changed++
		case "moved":
			result.Moved++
		case "unchanged":
			result.Unchanged++
		}
	}
	encoded, _ := json.Marshal(struct {
		Left, Right string
		Items       []ClassicMindMapCompareItem
	}{left.Digest, right.Digest, items})
	sum := sha256.Sum256(encoded)
	result.Digest = hex.EncodeToString(sum[:])
	return result, nil
}

func sortedClassicMindMapComparePaths(values map[string]classicMindMapComparableNode) []string {
	result := make([]string, 0, len(values))
	for path := range values {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

type classicMindMapComparableNode struct {
	Node      ClassicMindMapNode
	Signature string
}

func classicMindMapComparableNodes(doc ClassicMindMapDocument) map[string]classicMindMapComparableNode {
	byID := make(map[string]ClassicMindMapNode, len(doc.Nodes))
	children := make(map[string][]ClassicMindMapNode)
	for _, n := range doc.Nodes {
		byID[n.ID] = n
		children[n.ParentID] = append(children[n.ParentID], n)
	}
	for key := range children {
		sort.SliceStable(children[key], func(i, j int) bool {
			if children[key][i].Position != children[key][j].Position {
				return children[key][i].Position < children[key][j].Position
			}
			return children[key][i].ID < children[key][j].ID
		})
	}
	result := make(map[string]classicMindMapComparableNode, len(doc.Nodes))
	var walk func(string, string)
	walk = func(id, parentPath string) {
		siblings := children[byID[id].ParentID]
		occurrence := 0
		for _, s := range siblings {
			if normalizeClassicMindMapCompareLabel(s.Label) == normalizeClassicMindMapCompareLabel(byID[id].Label) {
				occurrence++
				if s.ID == id {
					break
				}
			}
		}
		segment := normalizeClassicMindMapCompareLabel(byID[id].Label)
		if id == doc.Map.RootNodeID {
			segment = "(корень)"
		}
		if occurrence > 1 {
			segment = fmt.Sprintf("%s[%d]", segment, occurrence)
		}
		path := segment
		if parentPath != "" {
			path = parentPath + " / " + segment
		}
		node := byID[id]
		result[path] = classicMindMapComparableNode{Node: node, Signature: classicMindMapNodeSignature(node)}
		for _, child := range children[id] {
			walk(child.ID, path)
		}
	}
	if root := byID[doc.Map.RootNodeID]; root.ID != "" {
		walk(root.ID, "")
	}
	return result
}

func normalizeClassicMindMapCompareLabel(value string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return -1
	}, value)), " ")
}
func classicMindMapNodeSignature(node ClassicMindMapNode) string {
	payload := struct {
		Label, Summary, Body string
		Kind                 ClassicMindMapNodeKind
	}{normalizeClassicMindMapCompareLabel(node.Label), strings.TrimSpace(node.Summary), strings.TrimSpace(node.BodyMarkdown), node.Kind}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func classicMindMapNodeChanges(left, right ClassicMindMapNode) []string {
	var out []string
	if left.Label != right.Label {
		out = append(out, "label")
	}
	if left.Kind != right.Kind {
		out = append(out, "kind")
	}
	if strings.TrimSpace(left.Summary) != strings.TrimSpace(right.Summary) {
		out = append(out, "summary")
	}
	if strings.TrimSpace(left.BodyMarkdown) != strings.TrimSpace(right.BodyMarkdown) {
		out = append(out, "body")
	}
	if classicMindMapSourcesDigest(left.Sources) != classicMindMapSourcesDigest(right.Sources) {
		out = append(out, "sources")
	}
	return out
}
func classicMindMapSourcesDigest(sources []ClassicMindMapSource) string {
	copySources := append([]ClassicMindMapSource(nil), sources...)
	for i := range copySources {
		copySources[i].ID = ""
		copySources[i].MapID = ""
		copySources[i].NodeID = ""
		copySources[i].Created = ""
		copySources[i].EvidenceState = ""
	}
	b, _ := json.Marshal(copySources)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func classicMindMapDocumentSummary(doc ClassicMindMapDocument) ClassicMindMapSummary {
	sources := 0
	for _, n := range doc.Nodes {
		sources += len(n.Sources)
	}
	return ClassicMindMapSummary{ClassicMindMap: doc.Map, NodeCount: len(doc.Nodes), SourceCount: sources}
}

func (s *Store) loadClassicMindMapBranch(request ClassicMindMapBranchRequest) (ClassicMindMapDocument, ClassicMindMapNode, []ClassicMindMapNode, error) {
	doc, err := s.LoadClassicMindMap(request.MapRef)
	if err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapNode{}, nil, err
	}
	if request.ExpectedRevision > 0 && doc.Map.Revision != request.ExpectedRevision {
		return ClassicMindMapDocument{}, ClassicMindMapNode{}, nil, ErrClassicMindMapRevisionConflict
	}
	if request.ExpectedDigest != "" && doc.Digest != request.ExpectedDigest {
		return ClassicMindMapDocument{}, ClassicMindMapNode{}, nil, fmt.Errorf("карта изменилась после выбора")
	}
	if request.ExpectedStateDigest != "" && doc.StateDigest != request.ExpectedStateDigest {
		return ClassicMindMapDocument{}, ClassicMindMapNode{}, nil, fmt.Errorf("состояние источников карты изменилось после выбора")
	}
	root, err := classicMindMapDocumentNodeRef(doc, request.NodeRef)
	if err != nil {
		return ClassicMindMapDocument{}, ClassicMindMapNode{}, nil, err
	}
	children := make(map[string][]ClassicMindMapNode)
	for _, node := range doc.Nodes {
		children[node.ParentID] = append(children[node.ParentID], node)
	}
	for key := range children {
		sort.SliceStable(children[key], func(i, j int) bool { return children[key][i].Position < children[key][j].Position })
	}
	nodes := make([]ClassicMindMapNode, 0)
	queue := []ClassicMindMapNode{root}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		nodes = append(nodes, current)
		queue = append(queue, children[current.ID]...)
	}
	return doc, root, nodes, nil
}

func classicMindMapDocumentNodeRef(doc ClassicMindMapDocument, ref string) (ClassicMindMapNode, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = doc.Map.RootNodeID
	}
	var matches []ClassicMindMapNode
	for _, node := range doc.Nodes {
		if node.ID == ref {
			return node, nil
		}
		if strings.EqualFold(node.Label, ref) {
			matches = append(matches, node)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return ClassicMindMapNode{}, ErrClassicMindMapAmbiguousRef
	}
	return ClassicMindMapNode{}, ErrClassicMindMapNodeNotFound
}

func (s *Store) currentClassicMindMapBranchEvidence(nodes []ClassicMindMapNode) ([]GroundedEvidence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]bool)
	result := make([]GroundedEvidence, 0)
	for _, node := range nodes {
		for _, source := range node.Sources {
			if source.Kind != ClassicMindMapSourceEvidence || source.Evidence == nil || source.EvidenceState != EvidenceCurrent {
				continue
			}
			anchor := source.Evidence
			if seen[anchor.CitationID] {
				continue
			}
			var entry Entry
			err := s.db.QueryRow(`SELECT id,text,document_id,document_revision,chunk_hash,source_file,source_path,page,block_index,block_chunk_index,block_total_chunks,chunk_index FROM entries WHERE document_id=? AND document_revision=? AND chunk_hash=? AND page=? AND block_index=? AND block_chunk_index=? LIMIT 1`, anchor.DocumentID, anchor.DocumentRevision, anchor.ChunkHash, anchor.Page, anchor.BlockIndex, anchor.BlockChunkIndex).Scan(&entry.ID, &entry.Text, &entry.DocumentID, &entry.DocumentRevision, &entry.ChunkHash, &entry.SourceFile, &entry.SourcePath, &entry.Page, &entry.BlockIndex, &entry.BlockChunkIndex, &entry.BlockTotalChunks, &entry.ChunkIndex)
			if err != nil {
				return nil, fmt.Errorf("загрузить current evidence %s: %w", anchor.CitationID, err)
			}
			e := groundedEvidenceForEntry(entry, anchor.Excerpt, DefaultAnswerLowConfidence)
			e.CitationID = anchor.CitationID
			e.EvidenceHash = anchor.EvidenceHash
			result = append(result, e)
			seen[anchor.CitationID] = true
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SourcePath != result[j].SourcePath {
			return result[i].SourcePath < result[j].SourcePath
		}
		if result[i].Page != result[j].Page {
			return result[i].Page < result[j].Page
		}
		if result[i].BlockIndex != result[j].BlockIndex {
			return result[i].BlockIndex < result[j].BlockIndex
		}
		return result[i].BlockChunkIndex < result[j].BlockChunkIndex
	})
	for i := range result {
		result[i].EvidenceRef = fmt.Sprintf("E%d", i+1)
	}
	return result, nil
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func writeClassicMindMapMarkdownSources(out *strings.Builder, sources []ClassicMindMapSource) {
	if len(sources) == 0 {
		return
	}
	out.WriteString("**Источники**\n\n")
	for _, source := range sources {
		label := firstNonEmpty(strings.TrimSpace(source.Title), strings.TrimSpace(source.Locator), strings.TrimSpace(source.URL), string(source.Kind))
		if source.Evidence != nil {
			anchor := source.Evidence
			label = firstNonEmpty(strings.TrimSpace(anchor.SourcePath), label)
			fmt.Fprintf(out, "- %s · стр. %d · блок %d · фрагмент %d · %s\n", label, anchor.Page, anchor.BlockIndex, anchor.BlockChunkIndex, source.EvidenceState)
			continue
		}
		fmt.Fprintf(out, "- %s · %s\n", label, source.EvidenceState)
	}
	out.WriteString("\n")
}

func shortClassicMindMapDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}
