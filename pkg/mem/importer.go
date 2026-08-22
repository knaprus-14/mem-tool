package mem

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/knaprus-14/mem-tool/pkg/ingest"
)

type ImportOptions struct {
	Title     string
	Tags      []string
	Important bool
	Progress  func(ingest.ProgressEvent)
}

type ImportResult struct {
	RunID            int64
	Status           string
	DocumentID       string
	DocumentRevision string
	SourcePath       string
	Blocks           int
	Chunks           int
	Pages            []int
	PhysicalPages    int
	StoredPages      int
	EmptyPages       int
	FailedPages      int
	Warnings         []string
}

// ImportDocument runs staged extraction and then indexes the resulting blocks.
func ImportDocument(ctx context.Context, cfg *Config, store *Store, path string, options ImportOptions) (ImportResult, error) {
	if ctx == nil {
		return ImportResult{}, fmt.Errorf("import context is nil")
	}
	if cfg == nil {
		return ImportResult{}, fmt.Errorf("import config is nil")
	}
	if store == nil {
		return ImportResult{}, fmt.Errorf("import store is nil")
	}
	runID, err := store.startDocumentImportRun(path)
	if err != nil {
		return ImportResult{}, err
	}
	latestStage := ingest.StageAnalyze
	progress := options.Progress
	options.Progress = func(event ingest.ProgressEvent) {
		if event.Stage != "" {
			latestStage = event.Stage
		}
		if progress != nil {
			progress(event)
		}
	}
	ocr := ingest.DefaultOptions().OCR
	if cfg.Ingest.OCRLanguages != "" {
		ocr.Languages = cfg.Ingest.OCRLanguages
	}
	if cfg.Ingest.TessdataDir != "" {
		ocr.TessdataDir = cfg.Ingest.TessdataDir
	}
	if cfg.Ingest.OCRDPI > 0 {
		ocr.DPI = cfg.Ingest.OCRDPI
	}
	if cfg.Ingest.LowConfidence > 0 {
		ocr.LowConfidence = cfg.Ingest.LowConfidence
	}
	doc, err := ingest.ExtractWithOptions(ctx, path, ingest.Options{
		Tools: ingest.ToolConfig{
			PDFToText: cfg.Ingest.PDFToText, MuTool: cfg.Ingest.MuTool,
			PDFInfo: cfg.Ingest.PDFInfo, PDFToPPM: cfg.Ingest.PDFToPPM, Python: cfg.Ingest.Python,
			DjVuText: cfg.Ingest.DjVuText, DjVuUsed: cfg.Ingest.DjVuUsed,
			DjVuRender: cfg.Ingest.DjVuRender, Tesseract: cfg.Ingest.Tesseract,
		},
		OCR: ocr, Progress: options.Progress,
	})
	if err != nil {
		result := importResultFromDocument(doc)
		result.RunID = runID
		result.Status = failedImportRunStatus(ctx, err)
		return result, finishFailedImportRun(store, runID, doc, result.Status, latestStage, err)
	}
	result, err := importExtractedDocumentWithContextEmbedderForRun(ctx, cfg, store, doc, options, GetEmbeddingContext, runID)
	if err != nil {
		result.RunID = runID
		result.Status = failedImportRunStatus(ctx, err)
		return result, finishFailedImportRun(store, runID, doc, result.Status, latestStage, err)
	}
	return result, nil
}

type importPiece struct {
	text             string
	label            string
	page             int
	blockIndex       int
	blockChunkIndex  int
	blockTotalChunks int
	marker           string
	extraction       string
	confidence       float64
	warnings         []string
}

func importExtractedDocumentWithEmbedder(cfg *Config, store *Store, doc ingest.Document,
	options ImportOptions, embed embeddingFunc) (ImportResult, error) {
	return importExtractedDocumentWithContextEmbedder(context.Background(), cfg, store, doc, options,
		func(_ context.Context, cfg *Config, text string) ([]float32, error) {
			return embed(cfg, text)
		})
}

type contextEmbeddingFunc func(context.Context, *Config, string) ([]float32, error)

func importExtractedDocumentWithContextEmbedder(ctx context.Context, cfg *Config, store *Store, doc ingest.Document,
	options ImportOptions, embed contextEmbeddingFunc) (ImportResult, error) {
	return importExtractedDocumentWithContextEmbedderForRun(ctx, cfg, store, doc, options, embed, 0)
}

func importExtractedDocumentWithContextEmbedderForRun(ctx context.Context, cfg *Config, store *Store, doc ingest.Document,
	options ImportOptions, embed contextEmbeddingFunc, runID int64) (ImportResult, error) {
	result := ImportResult{
		DocumentID: doc.ID, DocumentRevision: doc.Revision, SourcePath: doc.SourcePath,
		Blocks: len(doc.Blocks), Warnings: append([]string(nil), doc.Warnings...),
	}
	if ctx == nil {
		return result, fmt.Errorf("import context is nil")
	}
	if cfg == nil {
		return result, fmt.Errorf("import config is nil")
	}
	if store == nil {
		return result, fmt.Errorf("import store is nil")
	}
	if embed == nil {
		return result, fmt.Errorf("import embedder is nil")
	}
	embeddingIdentity, err := EmbeddingIdentityForConfig(cfg)
	if err != nil {
		return result, err
	}
	if err := ingest.ValidateDocument(doc); err != nil {
		return result, fmt.Errorf("invalid extracted document: %w", err)
	}

	var pieces []importPiece
	pageSet := make(map[int]bool)
	for _, block := range doc.Blocks {
		chunks := ChunkDocument(block.Text, cfg.Chunking.MaxSize, cfg.Chunking.Overlap, cfg.Chunking.Strategy)
		for blockChunkIndex, chunk := range chunks {
			label := block.Heading
			pieces = append(pieces, importPiece{
				text: chunk.Text, label: label, page: block.Page,
				blockIndex: block.Index, blockChunkIndex: blockChunkIndex,
				blockTotalChunks: len(chunks), marker: block.Marker,
				extraction: block.Extraction, confidence: block.OCRConfidence,
				warnings: append([]string(nil), block.Warnings...),
			})
		}
		if block.Page > 0 {
			pageSet[block.Page] = true
		}
	}
	if len(pieces) == 0 {
		return result, fmt.Errorf("document produced no chunks after chunking")
	}
	if options.Progress != nil {
		options.Progress(ingest.ProgressEvent{
			Stage: ingest.StageEmbed, Total: len(pieces),
			Message: fmt.Sprintf("подготовлено %d чанков; запись в базу начнётся только после всех embeddings", len(pieces)),
		})
	}

	embeddings := make([][]float32, len(pieces))
	for i, piece := range pieces {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("embedding cancelled before chunk %d/%d: %w; document not updated", i+1, len(pieces), err)
		}
		vector, err := embed(ctx, cfg, piece.text)
		if err != nil {
			return result, fmt.Errorf("embedding chunk %d/%d (page %d, block %d): %w; document not updated",
				i+1, len(pieces), piece.page, piece.blockIndex, err)
		}
		embeddings[i] = vector
		if options.Progress != nil {
			options.Progress(ingest.ProgressEvent{
				Stage: ingest.StageEmbed, Page: piece.page, Current: i + 1, Total: len(pieces),
				Message: fmt.Sprintf("embedding готов (вектор %d)", len(vector)),
			})
		}
	}

	title := options.Title
	if title == "" {
		title = doc.Title
	}
	storedChunks := make([]DocumentChunk, len(pieces))
	for i, piece := range pieces {
		storedChunks[i] = DocumentChunk{
			Text: piece.text, Title: title, Tags: options.Tags, Backend: embeddingIdentity.Backend,
			EmbeddingModel: embeddingIdentity.Model, EmbeddingSpace: embeddingIdentity.SpaceID,
			Embedding: embeddings[i], ChunkLabel: piece.label,
			ChunkIndex: i, TotalChunks: len(pieces), Important: options.Important,
			Provenance: Provenance{
				DocumentID: doc.ID, DocumentRevision: doc.Revision,
				ChunkHash: ChunkContentHash(piece.text), SourcePath: doc.SourcePath, MediaType: doc.MediaType,
				Page: piece.page, BlockIndex: piece.blockIndex, BlockMarker: piece.marker,
				BlockChunkIndex: piece.blockChunkIndex, BlockTotalChunks: piece.blockTotalChunks,
				ExtractionMethod: piece.extraction, OCRConfidence: piece.confidence,
				Warnings: piece.warnings,
			},
		}
	}
	var manifest *DocumentImportManifest
	if len(doc.PageManifest) > 0 {
		value := buildDocumentImportManifest(doc, pieces, storedChunks)
		manifest = &value
		result.PhysicalPages = value.PhysicalPageCount
		result.StoredPages = value.StoredPages
		result.EmptyPages = value.EmptyPages
		result.FailedPages = value.FailedPages
	}
	result.Status = DocumentImportRunSucceeded
	if result.FailedPages > 0 {
		result.Status = DocumentImportRunPartial
	}
	completion := successfulImportRunCompletion(runID, doc, result, len(storedChunks), manifest)
	if runID > 0 {
		if err := store.replaceDocumentChunksForRun(doc.SourcePath, storedChunks, manifest, &completion); err != nil {
			return result, fmt.Errorf("document not updated: %w", err)
		}
	} else if manifest != nil {
		if err := store.ReplaceDocumentChunksWithManifest(doc.SourcePath, storedChunks, *manifest); err != nil {
			return result, fmt.Errorf("document not updated: %w", err)
		}
	} else if err := store.ReplaceDocumentChunks(doc.SourcePath, storedChunks); err != nil {
		return result, fmt.Errorf("document not updated: %w", err)
	}
	result.RunID = runID
	result.Chunks = len(storedChunks)

	for page := range pageSet {
		result.Pages = append(result.Pages, page)
	}
	sort.Ints(result.Pages)
	return result, nil
}

func importResultFromDocument(doc ingest.Document) ImportResult {
	result := ImportResult{
		DocumentID: doc.ID, DocumentRevision: doc.Revision, SourcePath: doc.SourcePath,
		Blocks: len(doc.Blocks), Warnings: append([]string(nil), doc.Warnings...),
		PhysicalPages: doc.PhysicalPageCount,
	}
	for _, page := range doc.PageManifest {
		switch page.Status {
		case ingest.PageStatusStored:
			result.StoredPages++
		case ingest.PageStatusEmpty:
			result.EmptyPages++
		case ingest.PageStatusFailed:
			result.FailedPages++
		}
	}
	return result
}

func failedImportRunStatus(ctx context.Context, cause error) string {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || ctx.Err() != nil {
		return DocumentImportRunCancelled
	}
	return DocumentImportRunFailed
}

func finishFailedImportRun(store *Store, runID int64, doc ingest.Document, status, stage string, cause error) error {
	completion := failedImportRunCompletion(runID, doc, status, stage, cause)
	if err := store.finishDocumentImportRun(completion); err != nil {
		return fmt.Errorf("%w; журнал попытки импорта #%d не завершён: %v", cause, runID, err)
	}
	return cause
}

func failedImportRunCompletion(runID int64, doc ingest.Document, status, stage string, cause error) documentImportRunCompletion {
	completion := documentImportRunCompletion{
		RunID: runID, SourcePath: doc.SourcePath, DocumentID: doc.ID,
		DocumentRevision: doc.Revision, Format: string(doc.Format), MediaType: doc.MediaType,
		Status: status, FinalStage: stage, PhysicalPageCount: doc.PhysicalPageCount,
		SelectedPageFirst: doc.SelectedPageFirst, SelectedPageLast: doc.SelectedPageLast,
		Blocks: len(doc.Blocks), Warnings: append([]string(nil), doc.Warnings...),
		ErrorMessage: cause.Error(),
	}
	blockCounts := make(map[int]int)
	for _, block := range doc.Blocks {
		if block.Page > 0 {
			blockCounts[block.Page]++
		}
	}
	for _, sourcePage := range doc.PageManifest {
		page := DocumentImportPage{
			Page: sourcePage.Page, Status: string(sourcePage.Status), ExtractionMethod: sourcePage.Extraction,
			TextRunes: sourcePage.TextRunes, OCRConfidence: sourcePage.OCRConfidence,
			BlockCount: blockCounts[sourcePage.Page], Warnings: append([]string(nil), sourcePage.Warnings...),
		}
		switch page.Status {
		case DocumentImportPageStored:
			completion.StoredPages++
		case DocumentImportPageEmpty:
			completion.EmptyPages++
		case DocumentImportPageFailed:
			completion.FailedPages++
		}
		completion.Pages = append(completion.Pages, page)
	}
	return completion
}

func successfulImportRunCompletion(runID int64, doc ingest.Document, result ImportResult, chunks int,
	manifest *DocumentImportManifest) documentImportRunCompletion {
	completion := documentImportRunCompletion{
		RunID: runID, SourcePath: doc.SourcePath, DocumentID: doc.ID,
		DocumentRevision: doc.Revision, Format: string(doc.Format), MediaType: doc.MediaType,
		Status: result.Status, FinalStage: ingest.StageDone, DocumentUpdated: true,
		Blocks: result.Blocks, Chunks: chunks, Warnings: append([]string(nil), result.Warnings...),
	}
	if manifest != nil {
		completion.PhysicalPageCount = manifest.PhysicalPageCount
		completion.SelectedPageFirst = manifest.SelectedPageFirst
		completion.SelectedPageLast = manifest.SelectedPageLast
		completion.StoredPages = manifest.StoredPages
		completion.EmptyPages = manifest.EmptyPages
		completion.FailedPages = manifest.FailedPages
		completion.Pages = append([]DocumentImportPage(nil), manifest.Pages...)
	}
	return completion
}

func buildDocumentImportManifest(doc ingest.Document, pieces []importPiece, chunks []DocumentChunk) DocumentImportManifest {
	blockCounts := make(map[int]int)
	chunkCounts := make(map[int]int)
	for _, block := range doc.Blocks {
		if block.Page > 0 {
			blockCounts[block.Page]++
		}
	}
	for _, piece := range pieces {
		if piece.page > 0 {
			chunkCounts[piece.page]++
		}
	}
	manifest := DocumentImportManifest{
		Available: true, DocumentID: doc.ID, DocumentRevision: doc.Revision,
		SourcePath: doc.SourcePath, MediaType: doc.MediaType, Format: string(doc.Format),
		PhysicalPageCount: doc.PhysicalPageCount, SelectedPageFirst: doc.SelectedPageFirst,
		SelectedPageLast: doc.SelectedPageLast, Blocks: len(doc.Blocks), Chunks: len(chunks),
		Warnings: append([]string(nil), doc.Warnings...),
	}
	for _, sourcePage := range doc.PageManifest {
		page := DocumentImportPage{
			Page: sourcePage.Page, Status: string(sourcePage.Status), ExtractionMethod: sourcePage.Extraction,
			TextRunes: sourcePage.TextRunes, OCRConfidence: sourcePage.OCRConfidence,
			BlockCount: blockCounts[sourcePage.Page], ChunkCount: chunkCounts[sourcePage.Page],
			Warnings: append([]string(nil), sourcePage.Warnings...),
		}
		switch page.Status {
		case DocumentImportPageStored:
			manifest.StoredPages++
		case DocumentImportPageEmpty:
			manifest.EmptyPages++
		case DocumentImportPageFailed:
			manifest.FailedPages++
		}
		manifest.Pages = append(manifest.Pages, page)
	}
	return manifest
}
