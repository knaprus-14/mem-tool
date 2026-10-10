package mem

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/knaprus-14/mem-tool/internal/applog"
	"github.com/knaprus-14/mem-tool/internal/sqliteutil"
)

type sharedGraphKey struct{}
type sharedGraphContext struct {
	Origin, Root string
	Local        bool
}

type sharedGraphLifetimeKey struct{}

// sharedGraphLifetime fences background runners before their Store is closed.
// Closing a web session may race with a queued job entering its runner.
type sharedGraphLifetime struct {
	mu       sync.Mutex
	closing  bool
	runners  sync.WaitGroup
	exports  *knowledgeMapExportManager
	learning *knowledgeLearningJobManager
}

func (l *sharedGraphLifetime) begin() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing {
		return false
	}
	l.runners.Add(1)
	return true
}

func (l *sharedGraphLifetime) close() {
	l.mu.Lock()
	l.closing = true
	l.mu.Unlock()
	l.runners.Wait()
}

type sharedKnowledgeMapHandler struct {
	http.Handler
	cancel context.CancelFunc
	life   *sharedGraphLifetime
	once   sync.Once
}

// Close cancels queued work and waits for running jobs. The caller must first
// drain this handler's HTTP requests, then close the Store after Close returns.
func (h *sharedKnowledgeMapHandler) Close() error {
	h.once.Do(func() {
		h.cancel()
		h.life.close()
	})
	return nil
}

// CanEvict excludes sessions holding jobs or their downloadable results. An
// idle HTTP connection alone does not mean its background work is disposable.
func (h *sharedKnowledgeMapHandler) CanEvict() bool {
	if m := h.life.exports; m != nil {
		m.mu.Lock()
		busy := len(m.jobs) != 0
		m.mu.Unlock()
		if busy {
			return false
		}
	}
	if m := h.life.learning; m != nil {
		m.mu.Lock()
		busy := len(m.jobs) != 0
		m.mu.Unlock()
		if busy {
			return false
		}
	}
	return true
}

// OpenWebGraphIn opens an existing schema without import recovery, migrations or
// FTS maintenance. The bounded cache is the same shared Store used by graph CLI.
func OpenWebGraphIn(ctx context.Context, dir string) (s *Store, err error) {
	defer applog.Track("pkg.mem.OpenWebGraphIn")(&err)
	db, err := sqliteutil.OpenExisting(StorePathIn(dir))
	if err != nil {
		return nil, err
	}
	var count, size int64
	err = db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(text AS BLOB))+length(embedding)),0) FROM entries`).Scan(&count, &size)
	if err != nil || count > 20000 || size > 64<<20 {
		db.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrWebIndexLimit
	}
	s = &Store{db: db, path: StorePathIn(dir), lexicalDirty: true}
	if err = s.loadAll(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// NewSharedKnowledgeMapHandler must be mounted behind the application's owner
// and session checks on EVERY request, including downloads. It does not weaken
// the independent loopback handlers used by mem map open.
func NewSharedKnowledgeMapHandler(ctx context.Context, store *Store, title, token, origin, prefix string, local bool) (http.Handler, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || !strings.HasPrefix(prefix, "/graph/") || !strings.HasSuffix(prefix, "/") {
		return nil, errors.New("invalid shared graph mount")
	}
	cfg, err := LoadConfigIn(filepath.Dir(store.Path()))
	if err != nil {
		return nil, err
	}
	var selection *KnowledgeSelectionAnswerService
	answer := cfg.Answer.WithDefaults()
	answer.TimeoutSeconds = min(answer.TimeoutSeconds, 180)
	answer.MaxTokens = min(answer.MaxTokens, 16384)
	if provider, e := NewOllamaAnswerProvider(answer); e == nil {
		selection = &KnowledgeSelectionAnswerService{Provider: provider, Config: answer}
	}
	workspace := &KnowledgeMapWorkspace{SessionToken: token, ViewName: DefaultKnowledgeMapView, SelectionAnswer: selection != nil, APIBase: prefix + "api", ApplicationURL: "/#workspace=" + url.QueryEscape(strings.TrimSuffix(strings.TrimPrefix(prefix, "/graph/"), "/"))}
	life := &sharedGraphLifetime{}
	jobCtx, cancel := context.WithCancel(context.WithValue(ctx, sharedGraphLifetimeKey{}, life))
	h := newKnowledgeMapHandler(jobCtx, store, title, workspace, selection)
	return &sharedKnowledgeMapHandler{cancel: cancel, life: life, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, u.Host) || !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "forbidden", 403)
			return
		}
		if supplied := r.Header.Get("Origin"); supplied != "" && supplied != origin {
			http.Error(w, "forbidden", 403)
			return
		}
		clone := r.Clone(context.WithValue(r.Context(), sharedGraphKey{}, sharedGraphContext{Origin: origin, Root: filepath.Dir(filepath.Dir(store.Path())), Local: local}))
		clone.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, prefix)
		h.ServeHTTP(w, clone)
	})}, nil
}
