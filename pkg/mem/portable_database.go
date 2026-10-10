package mem

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/knaprus-14/mem-tool/internal/applog"
	"github.com/knaprus-14/mem-tool/internal/filelock"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

const MaxPortableBytes int64 = 2 << 30

var ErrPortableInvalid = errors.New("архив базы повреждён или несовместим; исходная база не изменена")
var ErrPortableBusy = errors.New("дождитесь завершения фоновых заданий перед переносом базы")

type PortableFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type PortableManifest struct {
	Version        int            `json:"version"`
	Created        string         `json:"created"`
	Files          []PortableFile `json:"files"`
	MissingSources int            `json:"missing_sources"`
}

func portableName(name string) bool {
	if name == "store.db" || name == "web-jobs.db" || name == "config.json" || name == "meta.json" {
		return true
	}
	return (strings.HasPrefix(name, "sources/") || strings.HasPrefix(name, "uploads/")) && fs.ValidPath(name) && !strings.ContainsAny(name, "\\:")
}
func snapshotDatabase(ctx context.Context, source, dest string) error {
	db, err := sqliteutil.OpenReadOnly(ctx, source)
	if err != nil {
		return err
	}
	defer db.Close()
	// Main remains mode=ro; disabling query_only permits a new VACUUM INTO file.
	if _, err = db.ExecContext(ctx, `PRAGMA query_only=OFF`); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `VACUUM INTO ?`, dest)
	return err
}
func portableConfig(cfg *Config) *Config {
	out := DefaultLocalConfig()
	out.Backend = cfg.Backend
	out.Ollama.Model = cfg.Ollama.Model
	out.Polza.Model = cfg.Polza.Model
	out.Answer.Model = cfg.Answer.Model
	out.Answer.TimeoutSeconds = cfg.Answer.TimeoutSeconds
	out.Answer.MaxTokens = cfg.Answer.MaxTokens
	out.Answer.ContextChars = cfg.Answer.ContextChars
	out.Answer.Temperature = cfg.Answer.Temperature
	out.Chunking = cfg.Chunking
	out.Ingest.OCRLanguages = cfg.Ingest.OCRLanguages
	out.Ingest.OCRDPI = cfg.Ingest.OCRDPI
	out.Ingest.LowConfidence = cfg.Ingest.LowConfidence
	out.Polza.APIKey = ""
	return out
}

// PreparePortableDatabaseIn creates consistent SQLite snapshots and verifies
// every physical source. Citation locators and immutable history are not edited.
func PreparePortableDatabaseIn(ctx context.Context, dir string, local bool) (archive string, manifest PortableManifest, cleanup func(), err error) {
	defer applog.Track("pkg.mem.PreparePortableDatabaseIn")(&err)
	cleanup = func() {}
	var locks []io.Closer
	// All locks are nonblocking; failure releases the acquired prefix.
	for _, name := range []string{"config.lock", "web-upload.lock", "web-worker.lock", "web-schema.lock", "web-index.lock", "web-map-ai.lock", "web-operations.lock"} {
		l, e := filelock.Try(filepath.Join(dir, name))
		if e != nil {
			for _, held := range locks {
				held.Close()
			}
			return "", manifest, cleanup, e
		}
		locks = append(locks, l)
	}
	defer func() {
		for _, l := range locks {
			l.Close()
		}
	}()
	tmp, err := os.MkdirTemp("", "mem-portable-")
	if err != nil {
		return "", manifest, cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	stage := filepath.Join(tmp, "data")
	if err = os.Mkdir(stage, 0700); err != nil {
		return
	}
	if err = snapshotDatabase(ctx, StorePathIn(dir), filepath.Join(stage, "store.db")); err != nil {
		return
	}
	db, err := sqliteutil.OpenExisting(filepath.Join(stage, "store.db"))
	if err != nil {
		return
	}
	defer db.Close()
	for _, check := range []struct{ table, query string }{{"web_operations", `SELECT COUNT(*) FROM web_operations WHERE status IN ('queued','running')`}, {"web_map_ai_tasks", `SELECT COUNT(*) FROM web_map_ai_tasks WHERE status IN ('queued','running')`}, {"web_index_generation", `SELECT COUNT(*) FROM web_index_generation WHERE status IN ('queued','running','preparing')`}} {
		var exists, count int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, check.table).Scan(&exists); err != nil {
			return
		}
		if exists > 0 {
			if err = db.QueryRowContext(ctx, check.query).Scan(&count); err != nil {
				return
			}
			if count > 0 {
				err = ErrPortableBusy
				return
			}
		}
	}
	if _, err = os.Stat(filepath.Join(dir, "web-jobs.db")); err == nil {
		if err = snapshotDatabase(ctx, filepath.Join(dir, "web-jobs.db"), filepath.Join(stage, "web-jobs.db")); err != nil {
			return
		}
		q, e := sqliteutil.OpenReadOnly(ctx, filepath.Join(stage, "web-jobs.db"))
		if e != nil {
			err = e
			return
		}
		var active int
		e = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running','cancelling')`).Scan(&active)
		q.Close()
		if e != nil {
			err = e
			return
		}
		if active > 0 {
			err = ErrPortableBusy
			return
		}
	} else if !os.IsNotExist(err) {
		return
	} else {
		err = nil
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		return
	}
	data, err := json.MarshalIndent(portableConfig(cfg), "", "  ")
	if err != nil {
		return
	}
	if err = os.WriteFile(filepath.Join(stage, "config.json"), data, 0600); err != nil {
		return
	}
	if data, e := os.ReadFile(MetaPathIn(dir)); e == nil && len(data) <= 64<<10 {
		if err = os.WriteFile(filepath.Join(stage, "meta.json"), data, 0600); err != nil {
			return
		}
	}
	// Read mappings from the source snapshot before replacing them with the
	// relative paths of this archive (supports repeated transfers).
	rows, err := db.QueryContext(ctx, `SELECT document_id,document_revision,source_path,file_hash FROM document_source_files ORDER BY document_id,document_revision LIMIT 5001`)
	if err != nil {
		return
	}
	type source struct {
		anchor EvidenceAnchor
		hash   string
	}
	sources := []source{}
	for rows.Next() {
		var s source
		if err = rows.Scan(&s.anchor.DocumentID, &s.anchor.DocumentRevision, &s.anchor.SourcePath, &s.hash); err != nil {
			rows.Close()
			return
		}
		sources = append(sources, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	if len(sources) > 5000 {
		err = ErrPortableInvalid
		return
	}
	sourceDB, err := openQueryDB(ctx, dir)
	if err != nil {
		return
	}
	defer sourceDB.Close()
	// Legacy entries without a fingerprint and external path-only attachments
	// cannot be silently advertised as portable, verified originals.
	var unavailable int
	if err = sourceDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT DISTINCT document_id,document_revision FROM entries WHERE document_id!='') e WHERE NOT EXISTS (SELECT 1 FROM document_source_files f WHERE f.document_id=e.document_id AND f.document_revision=e.document_revision)`).Scan(&unavailable); err != nil {
		return
	}
	manifest.MissingSources += unavailable
	var sourceTable int
	if err = sourceDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='mind_map_node_sources' AND type='table'`).Scan(&sourceTable); err != nil {
		return
	}
	if sourceTable != 0 {
		if err = sourceDB.QueryRowContext(ctx, `SELECT COUNT(DISTINCT locator) FROM mind_map_node_sources WHERE kind='external_file' AND locator NOT LIKE 'attachment:%'`).Scan(&unavailable); err != nil {
			return
		}
		manifest.MissingSources += unavailable
	}
	if _, err = db.ExecContext(ctx, sourceLocationsSchema+`DELETE FROM source_locations;`); err != nil {
		return
	}
	if err = os.Mkdir(filepath.Join(stage, "sources"), 0700); err != nil {
		return
	}
	var total int64
	for _, s := range sources {
		if err = ctx.Err(); err != nil {
			return
		}
		file, e := openLocatedSource(ctx, sourceDB, dir, s.anchor, local)
		if e != nil {
			manifest.MissingSources++
			continue
		}
		info, e := file.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
			file.Close()
			manifest.MissingSources++
			continue
		}
		snapshot, e := (&Store{db: sourceDB}).verifiedSourceFile(ctx, s.anchor, file)
		file.Close()
		if e != nil {
			manifest.MissingSources++
			continue
		}
		ext := strings.ToLower(path.Ext(strings.ReplaceAll(s.anchor.SourcePath, "\\", "/")))
		if len(ext) > 12 || strings.ContainsAny(ext, "/\\:") {
			ext = ".bin"
		}
		rel := "sources/" + strings.TrimPrefix(s.hash, "sha256:") + ext
		if !portableName(rel) {
			closeSourceSnapshot(snapshot)
			err = ErrPortableInvalid
			return
		}
		if _, e = os.Stat(filepath.Join(stage, filepath.FromSlash(rel))); os.IsNotExist(e) {
			total += info.Size()
			if total > MaxPortableBytes {
				closeSourceSnapshot(snapshot)
				err = ErrPortableInvalid
				return
			}
			out, e := os.OpenFile(filepath.Join(stage, filepath.FromSlash(rel)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e == nil {
				_, e = io.Copy(out, snapshot)
				ce := out.Close()
				if e == nil {
					e = ce
				}
			}
			if e != nil {
				closeSourceSnapshot(snapshot)
				err = e
				return
			}
		}
		closeSourceSnapshot(snapshot)
		if _, err = db.ExecContext(ctx, `INSERT INTO source_locations VALUES(?,?,?)`, s.anchor.DocumentID, s.anchor.DocumentRevision, rel); err != nil {
			return
		}
	}
	// Uploaded revisions retain queue-relative paths, independently of locators.
	if _, e := os.Stat(filepath.Join(stage, "web-jobs.db")); e == nil {
		q, e := sqliteutil.OpenReadOnly(ctx, filepath.Join(stage, "web-jobs.db"))
		if e != nil {
			err = e
			return
		}
		r, e := q.QueryContext(ctx, `SELECT relative_path,sha256 FROM sources`)
		if e != nil {
			q.Close()
			err = e
			return
		}
		uploads := map[string]string{}
		for r.Next() {
			var rel, hash string
			if e = r.Scan(&rel, &hash); e != nil {
				break
			}
			uploads[rel] = hash
		}
		if e == nil {
			e = r.Err()
		}
		r.Close()
		q.Close()
		if e != nil {
			err = e
			return
		}
		root, e := os.OpenRoot(dir)
		if e != nil {
			err = e
			return
		}
		defer root.Close()
		for rel, hash := range uploads {
			name := "uploads/" + rel
			if !portableName(name) {
				err = ErrPortableInvalid
				return
			}
			file, e := root.Open(filepath.FromSlash(name))
			if e != nil {
				manifest.MissingSources++
				continue
			}
			content, e := io.ReadAll(io.LimitReader(file, (64<<20)+1))
			file.Close()
			if e != nil || len(content) > 64<<20 {
				manifest.MissingSources++
				continue
			}
			actual := sha256.Sum256(content)
			if hex.EncodeToString(actual[:]) != strings.TrimPrefix(hash, "sha256:") {
				manifest.MissingSources++
				continue
			}
			total += int64(len(content))
			if total > MaxPortableBytes {
				err = ErrPortableInvalid
				return
			}
			p := filepath.Join(stage, filepath.FromSlash(name))
			if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				return
			}
			if err = os.WriteFile(p, content, 0600); err != nil {
				return
			}
		}
	}
	if err = db.Close(); err != nil {
		return
	}
	manifest.Version = 1
	manifest.Created = time.Now().UTC().Format(time.RFC3339Nano)
	manifest.Files = []PortableFile{}
	err = filepath.WalkDir(stage, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(stage, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if !portableName(rel) {
			return ErrPortableInvalid
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, f)
		f.Close()
		if e != nil {
			return e
		}
		manifest.Files = append(manifest.Files, PortableFile{rel, hex.EncodeToString(h.Sum(nil)), n})
		return nil
	})
	if err != nil {
		return
	}
	var archiveSize int64
	for _, f := range manifest.Files {
		archiveSize += f.Size
	}
	if archiveSize > MaxPortableBytes {
		err = ErrPortableInvalid
		return
	}
	archive = filepath.Join(tmp, "mem-database.zip")
	out, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	z := zip.NewWriter(out)
	for _, item := range manifest.Files {
		if err = ctx.Err(); err != nil {
			break
		}
		var target io.Writer
		target, err = z.Create(item.Path)
		if err != nil {
			break
		}
		f, e := os.Open(filepath.Join(stage, filepath.FromSlash(item.Path)))
		if e != nil {
			err = e
			break
		}
		_, err = io.Copy(target, f)
		f.Close()
		if err != nil {
			break
		}
	}
	if err == nil {
		var target io.Writer
		target, err = z.Create("manifest.json")
		if err == nil {
			err = json.NewEncoder(target).Encode(manifest)
		}
	}
	zErr := z.Close()
	closeErr := out.Close()
	if err == nil {
		err = zErr
	}
	if err == nil {
		err = closeErr
	}
	return
}

// RestorePortableDatabaseIn only accepts a fresh destination. It never replaces
// a live database. Hashes, sizes, entry names and SQLite integrity are checked
// before the caller registers the new workspace.
func RestorePortableDatabaseIn(ctx context.Context, archive, dir, owner string) (manifest PortableManifest, err error) {
	defer applog.Track("pkg.mem.RestorePortableDatabaseIn")(&err)
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		return manifest, ErrPortableInvalid
	}
	z, err := zip.OpenReader(archive)
	if err != nil {
		return manifest, ErrPortableInvalid
	}
	defer z.Close()
	if len(z.File) > 10001 {
		return manifest, ErrPortableInvalid
	}
	files := map[string]*zip.File{}
	var sum uint64
	for _, f := range z.File {
		if f.Name != "manifest.json" && !portableName(f.Name) || f.Mode()&os.ModeType != 0 || files[f.Name] != nil {
			return manifest, ErrPortableInvalid
		}
		sum += f.UncompressedSize64
		if f.UncompressedSize64 > uint64(MaxPortableBytes) || sum > uint64(MaxPortableBytes)+(1<<20) {
			return manifest, ErrPortableInvalid
		}
		files[f.Name] = f
	}
	m := files["manifest.json"]
	if m == nil || m.UncompressedSize64 > 1<<20 {
		return manifest, ErrPortableInvalid
	}
	r, err := m.Open()
	if err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(io.LimitReader(r, 1<<20))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&manifest)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = ErrPortableInvalid
		}
	}
	r.Close()
	if err != nil || manifest.Version != 1 || manifest.MissingSources < 0 || len(manifest.Files) != len(files)-1 {
		return manifest, ErrPortableInvalid
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return manifest, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	seen := map[string]bool{}
	for _, item := range manifest.Files {
		if err = ctx.Err(); err != nil {
			return
		}
		f := files[item.Path]
		if !portableName(item.Path) || seen[item.Path] || f == nil || item.Size < 0 || uint64(item.Size) != f.UncompressedSize64 {
			return manifest, ErrPortableInvalid
		}
		seen[item.Path] = true
		p := filepath.Join(dir, filepath.FromSlash(item.Path))
		if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return
		}
		source, e := f.Open()
		if e != nil {
			return manifest, e
		}
		dest, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			source.Close()
			return manifest, e
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(dest, h), io.LimitReader(source, item.Size+1))
		source.Close()
		ce := dest.Close()
		if e != nil || ce != nil || n != item.Size || hex.EncodeToString(h.Sum(nil)) != item.SHA256 {
			return manifest, ErrPortableInvalid
		}
	}
	if !seen["store.db"] || !seen["config.json"] {
		return manifest, ErrPortableInvalid
	}
	cfg, err := LoadConfigIn(dir)
	if err != nil {
		return manifest, ErrPortableInvalid
	}
	if err = SaveConfigIn(dir, portableConfig(cfg)); err != nil {
		return manifest, err
	}
	for _, name := range []string{"store.db", "web-jobs.db"} {
		if !seen[name] {
			continue
		}
		db, e := sqliteutil.OpenExisting(filepath.Join(dir, name))
		if e != nil {
			return manifest, ErrPortableInvalid
		}
		var integrity string
		e = db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity)
		if e != nil || integrity != "ok" {
			db.Close()
			return manifest, ErrPortableInvalid
		}
		if name == "store.db" {
			var entryTable int
			e = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='entries'`).Scan(&entryTable)
			if e != nil || entryTable != 1 {
				db.Close()
				return manifest, ErrPortableInvalid
			}
			for _, table := range []string{"web_map_ai_tasks", "web_index_generation", "web_operations"} {
				var count int
				e = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count)
				if e != nil {
					break
				}
				if count > 0 {
					_, e = db.ExecContext(ctx, `UPDATE `+table+` SET owner=?,status=CASE WHEN status IN ('queued','running','ready','preparing') THEN 'cancelled' ELSE status END`, owner)
					if e != nil {
						break
					}
				}
			}
			if e == nil {
				var count int
				e = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='web_operations'`).Scan(&count)
				if e == nil && count > 0 {
					_, e = db.ExecContext(ctx, `UPDATE web_operations SET local=0`)
				}
			}
		} else {
			_, e = db.ExecContext(ctx, `UPDATE sources SET owner=?`, owner)
		}
		ce := db.Close()
		if e != nil || ce != nil {
			return manifest, ErrPortableInvalid
		}
	}
	return manifest, nil
}
