package mem

import (
	"context"
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
	DocumentID       string
	DocumentRevision string
	SourcePath       string
	Blocks           int
	Chunks           int
	Pages            []int
	Warnings         []string
}

// ImportDocument runs staged extraction and then indexes the resulting blocks.
func ImportDocument(ctx context.Context, cfg *Config, store *Store, path string, options ImportOptions) (ImportResult, error) {
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
		return ImportResult{}, err
	}
	return importExtractedDocumentWithContextEmbedder(ctx, cfg, store, doc, options, GetEmbeddingContext)
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
	if err := store.ReplaceDocumentChunks(doc.SourcePath, storedChunks); err != nil {
		return result, fmt.Errorf("document not updated: %w", err)
	}
	result.Chunks = len(storedChunks)

	for page := range pageSet {
		result.Pages = append(result.Pages, page)
	}
	sort.Ints(result.Pages)
	return result, nil
}
