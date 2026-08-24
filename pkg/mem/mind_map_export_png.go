package mem

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"unicode"
)

const (
	MaxClassicMindMapPNGNodes  = 512
	MaxClassicMindMapPNGWidth  = 12000
	MaxClassicMindMapPNGHeight = 12000
	MaxClassicMindMapPNGPixels = 24_000_000
)

var ErrClassicMindMapPNGTooLarge = errors.New("classic mind map PNG canvas is too large")

func renderClassicMindMapPNG(model classicMindMapExportModel) ([]byte, error) {
	if len(model.Ordered) > MaxClassicMindMapPNGNodes || model.Width > MaxClassicMindMapPNGWidth ||
		model.Height > MaxClassicMindMapPNGHeight || int64(model.Width)*int64(model.Height) > MaxClassicMindMapPNGPixels {
		return nil, fmt.Errorf("%w: %d nodes require %dx%d pixels; use SVG or offline HTML for the complete scalable tree", ErrClassicMindMapPNGTooLarge, len(model.Ordered), model.Width, model.Height)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, model.Width, model.Height))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.RGBA{R: 245, G: 247, B: 251, A: 255}}, image.Point{}, draw.Src)
	edge := color.RGBA{R: 125, G: 140, B: 164, A: 255}
	for _, parent := range model.Ordered {
		for _, child := range parent.Children {
			x1, y1 := parent.X+classicMindMapExportNodeWidth, parent.Y+classicMindMapExportNodeHeight/2
			x2, y2 := child.X, child.Y+classicMindMapExportNodeHeight/2
			mid := (x1 + x2) / 2
			classicMindMapRasterLine(canvas, x1, y1, mid, y1, edge)
			classicMindMapRasterLine(canvas, mid, y1, mid, y2, edge)
			classicMindMapRasterLine(canvas, mid, y2, x2, y2, edge)
		}
	}
	classicMindMapRasterText(canvas, classicMindMapExportMargin, 24, model.Portable.Document.Map.Title, 2, color.RGBA{R: 22, G: 32, B: 51, A: 255})
	classicMindMapRasterText(canvas, classicMindMapExportMargin, 58,
		fmt.Sprintf("%d nodes / %d sources / revision %d / %s", model.Portable.NodeCount, model.Portable.SourceCount, model.Portable.Document.Map.Revision, model.Portable.MapDigest),
		1, color.RGBA{R: 82, G: 99, B: 126, A: 255})
	for _, item := range model.Ordered {
		classicMindMapRasterFillRect(canvas, item.X, item.Y, classicMindMapExportNodeWidth, classicMindMapExportNodeHeight, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		classicMindMapRasterRect(canvas, item.X, item.Y, classicMindMapExportNodeWidth, classicMindMapExportNodeHeight, color.RGBA{R: 174, G: 187, B: 208, A: 255})
		accent := classicMindMapExportPNGColor(item.Node.Kind)
		classicMindMapRasterFillRect(canvas, item.X, item.Y, 5, classicMindMapExportNodeHeight, accent)
		lines := classicMindMapExportWrap(item.Node.Label, 24, 2)
		for line, text := range lines {
			classicMindMapRasterText(canvas, item.X+16, item.Y+11+line*17, text, 2, color.RGBA{R: 22, G: 32, B: 51, A: 255})
		}
		classicMindMapRasterText(canvas, item.X+16, item.Y+classicMindMapExportNodeHeight-12,
			fmt.Sprintf("%s / %d sources", item.Node.Kind, len(item.Node.Sources)), 1, color.RGBA{R: 82, G: 99, B: 126, A: 255})
	}
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&encoded, canvas); err != nil {
		return nil, fmt.Errorf("encode classic mind map PNG: %w", err)
	}
	provenance, err := json.Marshal(model.Portable)
	if err != nil {
		return nil, fmt.Errorf("encode classic mind map PNG provenance: %w", err)
	}
	return classicMindMapPNGWithInternationalText(encoded.Bytes(), "mem-provenance", provenance)
}

func classicMindMapExportPNGColor(kind ClassicMindMapNodeKind) color.RGBA {
	switch kind {
	case ClassicMindMapNodeTopic:
		return color.RGBA{R: 54, G: 89, B: 217, A: 255}
	case ClassicMindMapNodeFact:
		return color.RGBA{R: 15, G: 139, B: 109, A: 255}
	case ClassicMindMapNodeQuestion:
		return color.RGBA{R: 176, G: 107, B: 19, A: 255}
	case ClassicMindMapNodeTask:
		return color.RGBA{R: 138, G: 75, B: 194, A: 255}
	case ClassicMindMapNodeDecision:
		return color.RGBA{R: 192, G: 61, B: 91, A: 255}
	default:
		return color.RGBA{R: 100, G: 116, B: 139, A: 255}
	}
}

func classicMindMapRasterFillRect(dst *image.RGBA, x, y, width, height int, value color.RGBA) {
	draw.Draw(dst, image.Rect(x, y, x+width, y+height).Intersect(dst.Bounds()), &image.Uniform{C: value}, image.Point{}, draw.Src)
}

func classicMindMapRasterRect(dst *image.RGBA, x, y, width, height int, value color.RGBA) {
	classicMindMapRasterLine(dst, x, y, x+width-1, y, value)
	classicMindMapRasterLine(dst, x, y+height-1, x+width-1, y+height-1, value)
	classicMindMapRasterLine(dst, x, y, x, y+height-1, value)
	classicMindMapRasterLine(dst, x+width-1, y, x+width-1, y+height-1, value)
}

func classicMindMapRasterLine(dst *image.RGBA, x0, y0, x1, y1 int, value color.RGBA) {
	dx, dy := int(math.Abs(float64(x1-x0))), -int(math.Abs(float64(y1-y0)))
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		if image.Pt(x0, y0).In(dst.Bounds()) {
			dst.SetRGBA(x0, y0, value)
		}
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func classicMindMapRasterText(dst *image.RGBA, x, y int, value string, scale int, ink color.RGBA) {
	if scale < 1 {
		scale = 1
	}
	start := x
	for _, raw := range value {
		if raw == '\n' {
			x = start
			y += 9 * scale
			continue
		}
		glyph := classicMindMapRasterGlyph(raw)
		for row, bits := range glyph {
			for column := 0; column < 5; column++ {
				if bits&(1<<uint(4-column)) == 0 {
					continue
				}
				classicMindMapRasterFillRect(dst, x+column*scale, y+row*scale, scale, scale, ink)
			}
		}
		x += 6 * scale
	}
}

func classicMindMapRasterGlyph(raw rune) [7]byte {
	r := unicode.ToUpper(raw)
	if alias, ok := classicMindMapRasterAliases[r]; ok {
		r = alias
	}
	if alias, ok := classicMindMapCyrillicAliases[r]; ok {
		r = alias
	}
	if glyph, ok := classicMindMapRasterGlyphs[r]; ok {
		return glyph
	}
	return classicMindMapRasterGlyphs['?']
}

var classicMindMapRasterAliases = map[rune]rune{
	'\u00a0': ' ',
	'—':      '-',
	'–':      '-',
	'−':      '-',
	'‑':      '-',
	'“':      '"',
	'”':      '"',
	'„':      '"',
	'’':      '\'',
	'‘':      '\'',
}

var classicMindMapCyrillicAliases = map[rune]rune{
	'А': 'A', 'В': 'B', 'С': 'C', 'Е': 'E', 'Н': 'H', 'К': 'K', 'М': 'M', 'О': 'O', 'Р': 'P', 'Т': 'T', 'Х': 'X',
}

var classicMindMapRasterGlyphs = map[rune][7]byte{
	' ': {0, 0, 0, 0, 0, 0, 0}, '!': {4, 4, 4, 4, 4, 0, 4}, '"': {10, 10, 10, 0, 0, 0, 0},
	'#': {10, 31, 10, 10, 31, 10, 0}, '%': {17, 2, 4, 8, 17, 0, 0}, '&': {12, 18, 20, 8, 21, 18, 13},
	'\'': {4, 4, 2, 0, 0, 0, 0}, '(': {2, 4, 8, 8, 8, 4, 2}, ')': {8, 4, 2, 2, 2, 4, 8},
	'*': {0, 10, 4, 31, 4, 10, 0}, '+': {0, 4, 4, 31, 4, 4, 0}, ',': {0, 0, 0, 0, 4, 4, 8},
	'-': {0, 0, 0, 31, 0, 0, 0}, '.': {0, 0, 0, 0, 0, 4, 4}, '/': {1, 2, 4, 8, 16, 0, 0},
	'0': {14, 17, 19, 21, 25, 17, 14}, '1': {4, 12, 4, 4, 4, 4, 14}, '2': {14, 17, 1, 2, 4, 8, 31},
	'3': {30, 1, 1, 14, 1, 1, 30}, '4': {2, 6, 10, 18, 31, 2, 2}, '5': {31, 16, 16, 30, 1, 1, 30},
	'6': {14, 16, 16, 30, 17, 17, 14}, '7': {31, 1, 2, 4, 8, 8, 8}, '8': {14, 17, 17, 14, 17, 17, 14},
	'9': {14, 17, 17, 15, 1, 1, 14}, ':': {0, 4, 4, 0, 4, 4, 0}, ';': {0, 4, 4, 0, 4, 4, 8},
	'<': {2, 4, 8, 16, 8, 4, 2}, '=': {0, 0, 31, 0, 31, 0, 0}, '>': {8, 4, 2, 1, 2, 4, 8},
	'?': {14, 17, 1, 2, 4, 0, 4}, '@': {14, 17, 23, 21, 23, 16, 14},
	'A': {14, 17, 17, 31, 17, 17, 17}, 'B': {30, 17, 17, 30, 17, 17, 30}, 'C': {14, 17, 16, 16, 16, 17, 14},
	'D': {28, 18, 17, 17, 17, 18, 28}, 'E': {31, 16, 16, 30, 16, 16, 31}, 'F': {31, 16, 16, 30, 16, 16, 16},
	'G': {14, 17, 16, 23, 17, 17, 15}, 'H': {17, 17, 17, 31, 17, 17, 17}, 'I': {14, 4, 4, 4, 4, 4, 14},
	'J': {7, 2, 2, 2, 2, 18, 12}, 'K': {17, 18, 20, 24, 20, 18, 17}, 'L': {16, 16, 16, 16, 16, 16, 31},
	'M': {17, 27, 21, 21, 17, 17, 17}, 'N': {17, 25, 21, 19, 17, 17, 17}, 'O': {14, 17, 17, 17, 17, 17, 14},
	'P': {30, 17, 17, 30, 16, 16, 16}, 'Q': {14, 17, 17, 17, 21, 18, 13}, 'R': {30, 17, 17, 30, 20, 18, 17},
	'S': {15, 16, 16, 14, 1, 1, 30}, 'T': {31, 4, 4, 4, 4, 4, 4}, 'U': {17, 17, 17, 17, 17, 17, 14},
	'V': {17, 17, 17, 17, 17, 10, 4}, 'W': {17, 17, 17, 21, 21, 21, 10}, 'X': {17, 17, 10, 4, 10, 17, 17},
	'Y': {17, 17, 10, 4, 4, 4, 4}, 'Z': {31, 1, 2, 4, 8, 16, 31},
	'[': {14, 8, 8, 8, 8, 8, 14}, '\\': {16, 8, 4, 2, 1, 0, 0}, ']': {14, 2, 2, 2, 2, 2, 14},
	'_': {0, 0, 0, 0, 0, 0, 31}, '|': {4, 4, 4, 4, 4, 4, 4},
	'«': {0, 5, 10, 20, 10, 5, 0}, '»': {0, 20, 10, 5, 10, 20, 0}, '…': {0, 0, 0, 0, 0, 21, 21},
	'№': {17, 25, 21, 19, 23, 0, 7},
	'Б': {31, 16, 16, 30, 17, 17, 30}, 'Г': {31, 16, 16, 16, 16, 16, 16}, 'Д': {6, 10, 10, 18, 18, 31, 17},
	'Ё': {10, 0, 31, 16, 30, 16, 31}, 'Ж': {21, 21, 14, 4, 14, 21, 21}, 'З': {14, 17, 1, 6, 1, 17, 14},
	'И': {17, 19, 21, 25, 17, 17, 17}, 'Й': {10, 17, 19, 21, 25, 17, 17}, 'Л': {7, 9, 9, 17, 17, 17, 17},
	'П': {31, 17, 17, 17, 17, 17, 17}, 'У': {17, 17, 10, 4, 4, 8, 16}, 'Ф': {4, 14, 21, 21, 14, 4, 4},
	'Ц': {17, 17, 17, 17, 17, 31, 1}, 'Ч': {17, 17, 17, 15, 1, 1, 1}, 'Ш': {21, 21, 21, 21, 21, 21, 31},
	'Щ': {21, 21, 21, 21, 21, 31, 1}, 'Ъ': {24, 8, 8, 14, 9, 9, 14}, 'Ы': {17, 17, 29, 21, 21, 21, 29},
	'Ь': {16, 16, 16, 30, 17, 17, 30}, 'Э': {14, 17, 1, 7, 1, 17, 14}, 'Ю': {22, 25, 25, 29, 25, 25, 22},
	'Я': {15, 17, 17, 15, 5, 9, 17},
}

func classicMindMapPNGWithInternationalText(encoded []byte, keyword string, text []byte) ([]byte, error) {
	if len(encoded) < 20 || !bytes.Equal(encoded[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) || len(keyword) == 0 || len(keyword) > 79 || strings.ContainsRune(keyword, 0) {
		return nil, errors.New("invalid PNG or iTXt keyword")
	}
	iend := encoded[len(encoded)-12:]
	if !bytes.Equal(iend[4:8], []byte("IEND")) {
		return nil, errors.New("encoded PNG does not end with IEND")
	}
	payload := make([]byte, 0, len(keyword)+5+len(text))
	payload = append(payload, keyword...)
	payload = append(payload, 0, 0, 0, 0, 0) // keyword NUL, uncompressed, method 0, empty language and translated keyword
	payload = append(payload, text...)
	result := make([]byte, 0, len(encoded)+len(payload)+12)
	result = append(result, encoded[:len(encoded)-12]...)
	result = appendClassicMindMapPNGChunk(result, "iTXt", payload)
	result = append(result, iend...)
	return result, nil
}

func appendClassicMindMapPNGChunk(dst []byte, kind string, data []byte) []byte {
	start := len(dst)
	dst = append(dst, make([]byte, 8)...)
	binary.BigEndian.PutUint32(dst[start:start+4], uint32(len(data)))
	copy(dst[start+4:start+8], kind)
	dst = append(dst, data...)
	checksum := crc32.NewIEEE()
	_, _ = checksum.Write(dst[start+4 : start+8])
	_, _ = checksum.Write(data)
	crc := checksum.Sum32()
	dst = append(dst, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(dst[len(dst)-4:], crc)
	return dst
}
