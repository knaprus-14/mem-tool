package mem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	KnowledgeMapProfileVersion           = 1
	DefaultKnowledgeMapProfileIterations = 3
	MaxKnowledgeMapProfileIterations     = 20
)

var ErrKnowledgeMapProfileChanged = errors.New("knowledge map changed during profile")

type KnowledgeMapProfileOptions struct {
	Iterations int    `json:"iterations"`
	ViewName   string `json:"view_name,omitempty"`
}

type KnowledgeMapProfileEnvironment struct {
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	GoVersion   string `json:"go_version"`
	LogicalCPUs int    `json:"logical_cpus"`
}

type KnowledgeMapProfileDatabase struct {
	Path          string `json:"path"`
	DataVersion   int64  `json:"data_version"`
	MainBytes     int64  `json:"main_bytes"`
	WALBytes      int64  `json:"wal_bytes"`
	SHMBytes      int64  `json:"shm_bytes"`
	PageCount     int64  `json:"page_count"`
	PageSize      int64  `json:"page_size"`
	FreelistPages int64  `json:"freelist_pages"`
	JournalMode   string `json:"journal_mode"`
	Entries       int64  `json:"entries"`
	Documents     int64  `json:"documents"`
}

type KnowledgeMapProfileSnapshot struct {
	GraphDigest         string `json:"graph_digest"`
	EvidenceStateDigest string `json:"evidence_state_digest"`
	ViewDigest          string `json:"view_digest"`
	HTMLDigest          string `json:"html_digest"`
	Nodes               int    `json:"nodes"`
	Edges               int    `json:"edges"`
	Evidence            int    `json:"evidence"`
	CurrentEvidence     int    `json:"current_evidence"`
	StaleEvidence       int    `json:"stale_evidence"`
	MissingEvidence     int    `json:"missing_evidence"`
	HTMLBytes           int64  `json:"html_bytes"`
}

type KnowledgeMapProfileStage struct {
	Name      string  `json:"name"`
	SamplesNS []int64 `json:"samples_ns"`
	MinNS     int64   `json:"min_ns"`
	MedianNS  int64   `json:"median_ns"`
	P95NS     int64   `json:"p95_ns"`
	MaxNS     int64   `json:"max_ns"`
	MeanNS    int64   `json:"mean_ns"`
}

type KnowledgeMapProfileReport struct {
	Version     int                            `json:"version"`
	Options     KnowledgeMapProfileOptions     `json:"options"`
	Environment KnowledgeMapProfileEnvironment `json:"environment"`
	Database    KnowledgeMapProfileDatabase    `json:"database"`
	Snapshot    KnowledgeMapProfileSnapshot    `json:"snapshot"`
	Stages      []KnowledgeMapProfileStage     `json:"stages"`
	Limitations []string                       `json:"limitations"`
}

// ProfileKnowledgeMap measures the host-side path used by the live map. Every
// sample is read-only. Content and evidence-state pins are checked before,
// during and after the run so timings from mixed snapshots are rejected.
func (s *Store) ProfileKnowledgeMap(options KnowledgeMapProfileOptions) (KnowledgeMapProfileReport, error) {
	if s == nil || s.db == nil {
		return KnowledgeMapProfileReport{}, errors.New("knowledge map profile store is unavailable")
	}
	if options.Iterations == 0 {
		options.Iterations = DefaultKnowledgeMapProfileIterations
	}
	if options.Iterations < 1 || options.Iterations > MaxKnowledgeMapProfileIterations {
		return KnowledgeMapProfileReport{}, fmt.Errorf("knowledge map profile iterations must be 1..%d", MaxKnowledgeMapProfileIterations)
	}
	options.ViewName = strings.TrimSpace(options.ViewName)
	if options.ViewName == "" {
		options.ViewName = DefaultKnowledgeMapView
	}
	if _, err := validateKnowledgeMapViewName(options.ViewName); err != nil {
		return KnowledgeMapProfileReport{}, err
	}
	dataVersionConn, err := s.db.Conn(context.Background())
	if err != nil {
		return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map data-version connection: %w", err)
	}
	defer dataVersionConn.Close()
	var initialDataVersion int64
	if err := dataVersionConn.QueryRowContext(context.Background(), `PRAGMA data_version`).Scan(&initialDataVersion); err != nil {
		return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map initial data_version: %w", err)
	}
	initialPin, err := s.BuildKnowledgeGraphExportPin()
	if err != nil {
		return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map initial pin: %w", err)
	}
	database, err := s.knowledgeMapProfileDatabase(initialDataVersion)
	if err != nil {
		return KnowledgeMapProfileReport{}, err
	}
	report := KnowledgeMapProfileReport{
		Version: KnowledgeMapProfileVersion, Options: options,
		Environment: KnowledgeMapProfileEnvironment{
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), LogicalCPUs: runtime.NumCPU(),
		},
		Database: database,
		Snapshot: KnowledgeMapProfileSnapshot{
			GraphDigest: initialPin.Digest, EvidenceStateDigest: initialPin.StateDigest,
			Nodes: initialPin.NodeCount, Edges: initialPin.EdgeCount, Evidence: initialPin.Evidence,
			CurrentEvidence: initialPin.Current, StaleEvidence: initialPin.Stale, MissingEvidence: initialPin.Missing,
		},
		Limitations: []string{
			"Измеряются только серверные чтения SQLite, разрешение evidence, сборка live-view и сериализация HTML.",
			"Браузерный JavaScript, layout, paint, задержка взаимодействия, нагрузка GPU и FPS не измеряются.",
			"Перед серией строится content/state pin, поэтому измеряется прогретый рабочий путь, а не холодный запуск процесса.",
			"Нулевой sample означает, что операция завершилась быстрее разрешения системного monotonic timer; на содержательной большой карте стадии должны измеряться отдельно от пустого smoke-test.",
			"Wall-clock зависит от компьютера, накопителя, состояния кэша и параллельных процессов; сравнивайте одинаковые запуски с совпадающими digest снимка.",
		},
	}

	loadSamples := make([]int64, 0, options.Iterations)
	for i := 0; i < options.Iterations; i++ {
		started := time.Now()
		graph, loadErr := s.LoadKnowledgeGraph()
		loadSamples = append(loadSamples, time.Since(started).Nanoseconds())
		if loadErr != nil {
			return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map graph load: %w", loadErr)
		}
		encoded, encodeErr := json.Marshal(graph)
		if encodeErr != nil {
			return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map graph digest: %w", encodeErr)
		}
		if prefixedSHA256(encoded) != initialPin.Digest {
			return KnowledgeMapProfileReport{}, fmt.Errorf("%w: graph content changed during load stage", ErrKnowledgeMapProfileChanged)
		}
	}
	report.Stages = append(report.Stages, summarizeKnowledgeMapProfileStage("sqlite_graph_load", loadSamples))

	pinSamples := make([]int64, 0, options.Iterations)
	for i := 0; i < options.Iterations; i++ {
		started := time.Now()
		snapshot, snapshotErr := s.buildKnowledgeGraphExportSnapshot()
		pinSamples = append(pinSamples, time.Since(started).Nanoseconds())
		if snapshotErr != nil {
			return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map evidence snapshot: %w", snapshotErr)
		}
		if snapshot.Pin.Digest != initialPin.Digest || snapshot.Pin.StateDigest != initialPin.StateDigest {
			return KnowledgeMapProfileReport{}, fmt.Errorf("%w: graph or evidence changed during snapshot stage", ErrKnowledgeMapProfileChanged)
		}
	}
	report.Stages = append(report.Stages, summarizeKnowledgeMapProfileStage("pinned_evidence_snapshot", pinSamples))

	viewSamples := make([]int64, 0, options.Iterations)
	var renderData KnowledgeMapViewData
	viewDigest := ""
	for i := 0; i < options.Iterations; i++ {
		started := time.Now()
		view, viewErr := s.BuildKnowledgeMapViewDataForView(options.ViewName)
		if viewErr != nil {
			return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map view assembly: %w", viewErr)
		}
		views, viewsErr := s.ListKnowledgeMapViews()
		if viewsErr != nil {
			return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map view list: %w", viewsErr)
		}
		view.Workspace = &KnowledgeMapWorkspace{
			SessionToken: strings.Repeat("p", 43), ViewName: options.ViewName, Views: views,
		}
		viewSamples = append(viewSamples, time.Since(started).Nanoseconds())
		if view.PortableExport == nil || view.PortableExport.Digest != initialPin.Digest || view.PortableExport.StateDigest != initialPin.StateDigest {
			return KnowledgeMapProfileReport{}, fmt.Errorf("%w: graph or evidence changed during view stage", ErrKnowledgeMapProfileChanged)
		}
		normalizeKnowledgeMapProfileView(&view)
		encoded, encodeErr := json.Marshal(view)
		if encodeErr != nil {
			return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map view digest: %w", encodeErr)
		}
		currentDigest := prefixedSHA256(encoded)
		if viewDigest == "" {
			viewDigest = currentDigest
			renderData = view
		} else if currentDigest != viewDigest {
			return KnowledgeMapProfileReport{}, fmt.Errorf("%w: live view changed during profile", ErrKnowledgeMapProfileChanged)
		}
	}
	report.Snapshot.ViewDigest = viewDigest
	report.Stages = append(report.Stages, summarizeKnowledgeMapProfileStage("live_view_assembly", viewSamples))

	renderSamples := make([]int64, 0, options.Iterations)
	htmlDigest := ""
	var htmlBytes int64
	for i := 0; i < options.Iterations; i++ {
		writer := newKnowledgeMapProfileHashWriter()
		started := time.Now()
		renderErr := WriteKnowledgeMapHTML(writer, "mem-tool — профиль карты", renderData)
		renderSamples = append(renderSamples, time.Since(started).Nanoseconds())
		if renderErr != nil {
			return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map HTML render: %w", renderErr)
		}
		currentDigest := writer.Digest()
		if htmlDigest == "" {
			htmlDigest, htmlBytes = currentDigest, writer.Bytes()
		} else if currentDigest != htmlDigest || writer.Bytes() != htmlBytes {
			return KnowledgeMapProfileReport{}, errors.New("knowledge map profile HTML render is not deterministic")
		}
	}
	report.Snapshot.HTMLDigest, report.Snapshot.HTMLBytes = htmlDigest, htmlBytes
	report.Stages = append(report.Stages, summarizeKnowledgeMapProfileStage("html_serialization", renderSamples))

	finalPin, err := s.BuildKnowledgeGraphExportPin()
	if err != nil {
		return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map final pin: %w", err)
	}
	if finalPin.Digest != initialPin.Digest || finalPin.StateDigest != initialPin.StateDigest {
		return KnowledgeMapProfileReport{}, fmt.Errorf("%w: graph or evidence changed before profile completed", ErrKnowledgeMapProfileChanged)
	}
	var finalDataVersion int64
	if err := dataVersionConn.QueryRowContext(context.Background(), `PRAGMA data_version`).Scan(&finalDataVersion); err != nil {
		return KnowledgeMapProfileReport{}, fmt.Errorf("profile knowledge map final data_version: %w", err)
	}
	if finalDataVersion != initialDataVersion {
		return KnowledgeMapProfileReport{}, fmt.Errorf("%w: SQLite data_version changed from %d to %d", ErrKnowledgeMapProfileChanged, initialDataVersion, finalDataVersion)
	}
	return report, nil
}

func normalizeKnowledgeMapProfileView(view *KnowledgeMapViewData) {
	if view == nil {
		return
	}
	// These fields describe request time, not map content. Leaving them in the
	// digest would make a profile that crosses a wall-clock second look stale.
	view.GeneratedAt = ""
	view.RevisionDiff.GeneratedAt = ""
}

func (s *Store) knowledgeMapProfileDatabase(dataVersion int64) (KnowledgeMapProfileDatabase, error) {
	if s == nil || s.db == nil {
		return KnowledgeMapProfileDatabase{}, errors.New("knowledge map profile store is unavailable")
	}
	result := KnowledgeMapProfileDatabase{Path: s.Path(), DataVersion: dataVersion}
	fileSize := func(path string) (int64, error) {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		return info.Size(), nil
	}
	var err error
	if result.MainBytes, err = fileSize(result.Path); err != nil {
		return KnowledgeMapProfileDatabase{}, fmt.Errorf("profile knowledge map database size: %w", err)
	}
	if result.WALBytes, err = fileSize(result.Path + "-wal"); err != nil {
		return KnowledgeMapProfileDatabase{}, fmt.Errorf("profile knowledge map WAL size: %w", err)
	}
	if result.SHMBytes, err = fileSize(result.Path + "-shm"); err != nil {
		return KnowledgeMapProfileDatabase{}, fmt.Errorf("profile knowledge map SHM size: %w", err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.db.QueryRow(`PRAGMA page_count`).Scan(&result.PageCount); err != nil {
		return KnowledgeMapProfileDatabase{}, fmt.Errorf("profile knowledge map page_count: %w", err)
	}
	if err := s.db.QueryRow(`PRAGMA page_size`).Scan(&result.PageSize); err != nil {
		return KnowledgeMapProfileDatabase{}, fmt.Errorf("profile knowledge map page_size: %w", err)
	}
	if err := s.db.QueryRow(`PRAGMA freelist_count`).Scan(&result.FreelistPages); err != nil {
		return KnowledgeMapProfileDatabase{}, fmt.Errorf("profile knowledge map freelist_count: %w", err)
	}
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&result.JournalMode); err != nil {
		return KnowledgeMapProfileDatabase{}, fmt.Errorf("profile knowledge map journal_mode: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&result.Entries); err != nil {
		return KnowledgeMapProfileDatabase{}, fmt.Errorf("profile knowledge map entries: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT document_id) FROM entries WHERE document_id <> ''`).Scan(&result.Documents); err != nil {
		return KnowledgeMapProfileDatabase{}, fmt.Errorf("profile knowledge map documents: %w", err)
	}
	return result, nil
}

func summarizeKnowledgeMapProfileStage(name string, samples []int64) KnowledgeMapProfileStage {
	ordered := append([]int64(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	stage := KnowledgeMapProfileStage{Name: name, SamplesNS: append([]int64(nil), samples...)}
	if len(ordered) == 0 {
		return stage
	}
	stage.MinNS, stage.MaxNS = ordered[0], ordered[len(ordered)-1]
	middle := len(ordered) / 2
	if len(ordered)%2 == 0 {
		stage.MedianNS = ordered[middle-1] + (ordered[middle]-ordered[middle-1])/2
	} else {
		stage.MedianNS = ordered[middle]
	}
	p95Index := int(math.Ceil(float64(len(ordered))*0.95)) - 1
	if p95Index < 0 {
		p95Index = 0
	}
	stage.P95NS = ordered[p95Index]
	var total int64
	for _, sample := range ordered {
		total += sample
	}
	stage.MeanNS = total / int64(len(ordered))
	return stage
}

type knowledgeMapProfileHashWriter struct {
	hash  hash.Hash
	bytes int64
}

func newKnowledgeMapProfileHashWriter() *knowledgeMapProfileHashWriter {
	return &knowledgeMapProfileHashWriter{hash: sha256.New()}
}

func (w *knowledgeMapProfileHashWriter) Write(data []byte) (int, error) {
	n, err := w.hash.Write(data)
	w.bytes += int64(n)
	return n, err
}

func (w *knowledgeMapProfileHashWriter) Digest() string {
	return "sha256:" + hex.EncodeToString(w.hash.Sum(nil))
}

func (w *knowledgeMapProfileHashWriter) Bytes() int64 {
	return w.bytes
}

var _ io.Writer = (*knowledgeMapProfileHashWriter)(nil)
