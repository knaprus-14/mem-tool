package mem

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSharedGraphCloseDrainsExportAndFencesLateRunner(t *testing.T) {
	store, _ := graphStoreAndAnchor(t)
	defer store.Close()
	life := &sharedGraphLifetime{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), sharedGraphLifetimeKey{}, life))
	h := &sharedKnowledgeMapHandler{life: life, cancel: cancel}
	h.Handler = newKnowledgeMapHandler(ctx, store, "test", &KnowledgeMapWorkspace{SessionToken: "test"}, nil)
	defer h.Close()
	if !h.CanEvict() {
		t.Fatal("empty handler cannot be evicted")
	}
	// Block the real runner at its Store snapshot after it has acquired its lease.
	store.mu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(store.mu.Unlock) }
	defer unlock()
	job, err := life.exports.start(testKnowledgeMapExportJobRequest())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		life.exports.mu.Lock()
		phase := life.exports.jobs[job.ID].view.Phase
		life.exports.mu.Unlock()
		if phase == "pinning" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("export did not reach Store snapshot")
		}
		time.Sleep(time.Millisecond)
	}
	if h.CanEvict() {
		t.Fatal("handler with running export is evictable")
	}
	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	<-ctx.Done()
	select {
	case <-closed:
		t.Fatal("Close returned while runner still owns Store")
	default:
	}
	unlock()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("export did not drain")
	}
	waitKnowledgeMapExportJobStatus(t, life.exports, job.ID, KnowledgeMapExportJobCancelled)
	// A worker already dequeued at cancellation must fail before touching Store.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = life.exports.runner(context.Background(), testKnowledgeMapExportJobRequest(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("late runner error: %v", err)
	}
}

type sharedGraphBlockingProvider struct {
	started, cancelled, release chan struct{}
}

func (p *sharedGraphBlockingProvider) Generate(ctx context.Context, _ AnswerRequest) (string, error) {
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	<-p.release
	return "", ctx.Err()
}

func TestSharedGraphCloseDrainsLearningProvider(t *testing.T) {
	store, anchor := graphStoreAndAnchor(t)
	defer store.Close()
	const id = "lifecycle-learning"
	if err := store.UpsertKnowledgeGraph(KnowledgeGraph{Nodes: []KnowledgeNode{{ID: id, Kind: KnowledgeNodeDefinition, Label: "Definition", Status: KnowledgeStatusActive, Origin: KnowledgeOriginGenerated, Evidence: []EvidenceAnchor{anchor}}}}); err != nil {
		t.Fatal(err)
	}
	selection := KnowledgeSelectionRequest{NodeIDs: []string{id}}
	manifest, err := store.BuildKnowledgeSelectionManifest(selection)
	if err != nil {
		t.Fatal(err)
	}
	life := &sharedGraphLifetime{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), sharedGraphLifetimeKey{}, life))
	p := &sharedGraphBlockingProvider{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	service := &KnowledgeSelectionAnswerService{Provider: p, Config: AnswerConfig{Model: "test", MaxTokens: 512, ContextChars: 12000}}
	h := &sharedKnowledgeMapHandler{life: life, cancel: cancel}
	h.Handler = newKnowledgeMapHandler(ctx, store, "test", &KnowledgeMapWorkspace{SessionToken: "test"}, service)
	defer h.Close()
	var once sync.Once
	release := func() { once.Do(func() { close(p.release) }) }
	defer release()
	job, err := life.learning.start(KnowledgeLearningGenerateRequest{Selection: selection, ExpectedManifestDigest: manifest.Digest, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.started:
	case <-time.After(5 * time.Second):
		t.Fatal("learning provider did not start")
	}
	if h.CanEvict() {
		t.Fatal("running learning session is evictable")
	}
	closed := make(chan struct{})
	go func() { h.Close(); close(closed) }()
	select {
	case <-p.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not receive cancellation")
	}
	select {
	case <-closed:
		t.Fatal("Close returned before provider drained")
	default:
	}
	release()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("learning runner did not drain")
	}
	waitKnowledgeLearningJobStatus(t, life.learning, job.ID, KnowledgeLearningJobCancelled)
}

func TestSharedGraphLRUPreservesCompletedExport(t *testing.T) {
	store, _ := graphStoreAndAnchor(t)
	defer store.Close()
	life := &sharedGraphLifetime{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), sharedGraphLifetimeKey{}, life))
	h := &sharedKnowledgeMapHandler{life: life, cancel: cancel}
	h.Handler = newKnowledgeMapHandler(ctx, store, "test", &KnowledgeMapWorkspace{SessionToken: "test"}, nil)
	defer h.Close()
	snapshot, err := store.buildKnowledgeGraphExportSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	request := testKnowledgeMapExportJobRequest()
	request.ExpectedDigest, request.ExpectedStateDigest = snapshot.Pin.Digest, snapshot.Pin.StateDigest
	job, err := life.exports.start(request)
	if err != nil {
		t.Fatal(err)
	}
	waitKnowledgeMapExportJobStatus(t, life.exports, job.ID, KnowledgeMapExportJobCompleted)
	if h.CanEvict() {
		t.Fatal("completed undownloaded artifact may be discarded by LRU")
	}
	if _, err = life.exports.artifact(job.ID); err != nil {
		t.Fatal("export artifact lost", err)
	}
}
