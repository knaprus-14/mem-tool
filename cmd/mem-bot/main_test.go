package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func privateTestUpdate(userID, chatID int64, chatType models.ChatType) *models.Update {
	return &models.Update{Message: &models.Message{
		From: &models.User{ID: userID},
		Chat: models.Chat{ID: chatID, Type: chatType},
		Text: "test",
	}}
}

type fakeTelegramSender struct {
	errors []error
	params []*bot.SendMessageParams
}

func (s *fakeTelegramSender) SendMessage(_ context.Context, params *bot.SendMessageParams) (*models.Message, error) {
	copyParams := *params
	s.params = append(s.params, &copyParams)
	call := len(s.params) - 1
	if call < len(s.errors) && s.errors[call] != nil {
		return nil, s.errors[call]
	}
	return &models.Message{}, nil
}

func TestVersionCommandRequiresExactArity(t *testing.T) {
	for _, command := range []string{"version", "--version", "-v"} {
		isVersion, err := parseBotVersionCommand([]string{command})
		if !isVersion || err != nil {
			t.Errorf("parseBotVersionCommand(%q) = (%v, %v)", command, isVersion, err)
		}
		isVersion, err = parseBotVersionCommand([]string{command, "unexpected"})
		if !isVersion || err == nil {
			t.Errorf("parseBotVersionCommand(%q with tail) = (%v, %v)", command, isVersion, err)
		}
	}
	if isVersion, err := parseBotVersionCommand([]string{"unknown"}); isVersion || err != nil {
		t.Fatalf("non-version command = (%v, %v)", isVersion, err)
	}
}

func TestPrivateMessageRejectsUnsupportedUpdates(t *testing.T) {
	updates := []*models.Update{
		nil,
		{},
		{Message: &models.Message{Chat: models.Chat{Type: models.ChatTypePrivate}}},
		privateTestUpdate(0, 0, models.ChatTypePrivate),
		privateTestUpdate(7, -100, models.ChatTypeGroup),
		privateTestUpdate(7, -100, models.ChatTypeSupergroup),
		privateTestUpdate(7, -100, models.ChatTypeChannel),
	}
	for i, update := range updates {
		if _, ok := privateMessage(update); ok {
			t.Errorf("unsupported update %d was accepted: %#v", i, update)
		}
	}
	if message, ok := privateMessage(privateTestUpdate(7, 7, models.ChatTypePrivate)); !ok || message.From.ID != 7 {
		t.Fatalf("valid private message rejected: ok=%v message=%#v", ok, message)
	}
}

func TestHandlersIgnoreNilAndGroupUpdatesWithoutPanic(t *testing.T) {
	// Each handler validates the update before dereferencing Message, From or Bot.
	for _, update := range []*models.Update{nil, privateTestUpdate(7, -100, models.ChatTypeGroup)} {
		cmdStart(context.Background(), nil, update)
		cmdHelp(context.Background(), nil, update)
		cmdAdd(context.Background(), nil, update)
		cmdSearch(context.Background(), nil, update)
		cmdRecent(context.Background(), nil, update)
		cmdStats(context.Background(), nil, update)
		cmdImport(context.Background(), nil, update)
		defaultHandler(context.Background(), nil, update)
	}
}

func TestAuthorizeUpdateEnforcesPrivateAllowlistAndRateLimit(t *testing.T) {
	d := &botData{
		allowedUsers:       map[int64]struct{}{7: {}},
		rateLimitPerMinute: 2,
		rateWindows:        make(map[int64]requestWindow),
	}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if _, decision := d.authorizeUpdate(privateTestUpdate(7, -100, models.ChatTypeGroup), now); decision != accessNonPrivateChat {
		t.Fatalf("group decision = %v", decision)
	}
	if _, decision := d.authorizeUpdate(privateTestUpdate(8, 8, models.ChatTypePrivate), now); decision != accessUnauthorized {
		t.Fatalf("unknown-user decision = %v", decision)
	}
	for i := 0; i < 2; i++ {
		if _, decision := d.authorizeUpdate(privateTestUpdate(7, 7, models.ChatTypePrivate), now); decision != accessAllowed {
			t.Fatalf("allowed request %d decision = %v", i+1, decision)
		}
	}
	if message, decision := d.authorizeUpdate(privateTestUpdate(7, 7, models.ChatTypePrivate), now); decision != accessRateLimited || message == nil {
		t.Fatalf("third request: decision=%v message=%#v", decision, message)
	}
	if message, decision := d.authorizeUpdate(privateTestUpdate(7, 7, models.ChatTypePrivate), now); decision != accessRateLimited || message != nil {
		t.Fatalf("repeated limited request should be silent: decision=%v message=%#v", decision, message)
	}
	if _, decision := d.authorizeUpdate(privateTestUpdate(7, 7, models.ChatTypePrivate), now.Add(time.Minute)); decision != accessAllowed {
		t.Fatalf("new window decision = %v", decision)
	}
}

func TestGuardedHandlerNeverDispatchesInvalidGroupOrUnknownUser(t *testing.T) {
	originalData := data
	defer func() { data = originalData }()
	data = &botData{allowedUsers: map[int64]struct{}{7: {}}, rateWindows: make(map[int64]requestWindow)}
	calls := 0
	handler := guardedHandler(func(context.Context, *bot.Bot, *models.Update) { calls++ })
	handler(context.Background(), nil, nil)
	handler(context.Background(), nil, privateTestUpdate(7, -100, models.ChatTypeGroup))
	handler(context.Background(), nil, privateTestUpdate(8, 8, models.ChatTypePrivate))
	if calls != 0 {
		t.Fatalf("guard dispatched %d rejected update(s)", calls)
	}
	handler(context.Background(), nil, privateTestUpdate(7, 7, models.ChatTypePrivate))
	if calls != 1 {
		t.Fatalf("guard dispatched valid private update %d times", calls)
	}
}

func TestExpensiveAdmissionSaturationLeavesWorkerForFastCommand(t *testing.T) {
	originalData := data
	data = &botData{expensiveLimit: botExpensiveWorkers, expensiveUserLimit: botExpensivePerUser}
	t.Cleanup(func() { data = originalData })

	jobs := make(chan func(), botWorkers*3)
	blockExpensive := make(chan struct{})
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(blockExpensive) }) }
	var workers sync.WaitGroup
	for range botWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				job()
			}
		}()
	}
	t.Cleanup(func() {
		unblock()
		close(jobs)
		workers.Wait()
	})

	started := make(chan int64, botExpensiveWorkers)
	expensive := expensiveHandlerWithNotifier(func(_ context.Context, _ *bot.Bot, update *models.Update) {
		started <- update.Message.From.ID
		<-blockExpensive
	}, func(context.Context, *bot.Bot, int64) {})
	for userID := int64(1); userID <= botExpensiveWorkers; userID++ {
		update := privateTestUpdate(userID, userID, models.ChatTypePrivate)
		jobs <- func() { expensive(context.Background(), nil, update) }
	}
	for range botExpensiveWorkers {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("expensive worker did not start")
		}
	}

	// Saturated expensive requests return immediately instead of occupying the
	// two workers reserved for lightweight commands.
	for userID := int64(100); userID < 100+botWorkers*2; userID++ {
		update := privateTestUpdate(userID, userID, models.ChatTypePrivate)
		jobs <- func() { expensive(context.Background(), nil, update) }
	}
	fastDone := make(chan struct{}, 1)
	fast := bot.HandlerFunc(func(context.Context, *bot.Bot, *models.Update) { fastDone <- struct{}{} })
	jobs <- func() { fast(context.Background(), nil, privateTestUpdate(99, 99, models.ChatTypePrivate)) }
	select {
	case <-fastDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fast command was blocked by saturated expensive work")
	}
	unblock()
}

func TestExpensiveAdmissionRejectsConcurrentWorkFromSameUser(t *testing.T) {
	originalData := data
	data = &botData{expensiveLimit: botExpensiveWorkers, expensiveUserLimit: botExpensivePerUser}
	t.Cleanup(func() { data = originalData })

	blockFirst := make(chan struct{})
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(blockFirst) }) }
	t.Cleanup(unblock)
	started := make(chan int64, 2)
	busy := make(chan int64, 1)
	handler := expensiveHandlerWithNotifier(func(_ context.Context, _ *bot.Bot, update *models.Update) {
		started <- update.Message.From.ID
		<-blockFirst
	}, func(_ context.Context, _ *bot.Bot, chatID int64) {
		busy <- chatID
	})

	firstDone := make(chan struct{})
	go func() {
		handler(context.Background(), nil, privateTestUpdate(7, 7, models.ChatTypePrivate))
		close(firstDone)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first same-user request did not start")
	}

	secondDone := make(chan struct{})
	go func() {
		handler(context.Background(), nil, privateTestUpdate(7, 7, models.ChatTypePrivate))
		close(secondDone)
	}()
	select {
	case chatID := <-busy:
		if chatID != 7 {
			t.Fatalf("busy response chat = %d, want 7", chatID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("same-user request waited instead of returning busy")
	}
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("rejected same-user request did not return")
	}

	unblock()
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not release its admission")
	}

	// Once the first handler exits, the same user can be admitted again.
	retryDone := make(chan struct{})
	go func() {
		handler(context.Background(), nil, privateTestUpdate(7, 7, models.ChatTypePrivate))
		close(retryDone)
	}()
	select {
	case userID := <-started:
		if userID != 7 {
			t.Fatalf("re-admitted user = %d, want 7", userID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("same user was not re-admitted after release")
	}
	select {
	case <-retryDone:
	case <-time.After(2 * time.Second):
		t.Fatal("re-admitted handler did not finish")
	}
}

func TestAuthorizeUpdateBoundsRateWindowState(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	d := &botData{allowPublic: true, rateLimitPerMinute: 1, globalRateLimit: maxRateWindowEntries + 10, rateWindows: make(map[int64]requestWindow, maxRateWindowEntries)}
	for id := int64(1); id <= maxRateWindowEntries; id++ {
		d.rateWindows[id] = requestWindow{started: now, count: 1}
	}
	if _, decision := d.authorizeUpdate(privateTestUpdate(maxRateWindowEntries+1, maxRateWindowEntries+1, models.ChatTypePrivate), now); decision != accessUnauthorized {
		t.Fatalf("new rate-window entry decision = %v", decision)
	}
	if len(d.rateWindows) != maxRateWindowEntries {
		t.Fatalf("rate window map grew to %d", len(d.rateWindows))
	}
	if _, decision := d.authorizeUpdate(privateTestUpdate(maxRateWindowEntries+1, maxRateWindowEntries+1, models.ChatTypePrivate), now.Add(time.Minute)); decision != accessAllowed {
		t.Fatalf("expired windows were not swept: decision=%v", decision)
	}
}

func TestEmptyAllowlistFailsClosedUnlessPublicModeIsExplicit(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	d := &botData{}
	if _, decision := d.authorizeUpdate(privateTestUpdate(7, 7, models.ChatTypePrivate), now); decision != accessUnauthorized {
		t.Fatalf("empty allowlist decision = %v", decision)
	}
	if err := validateBotAccessConfig(nil, false, 30, 120, 10_000, 100); err == nil {
		t.Fatal("empty allowlist without public opt-in was accepted")
	}
	if err := validateBotAccessConfig(nil, true, 30, 120, 10_000, 100); err != nil {
		t.Fatalf("bounded explicit public mode rejected: %v", err)
	}
	if err := validateBotAccessConfig(nil, true, 30, 0, 10_000, 100); err == nil {
		t.Fatal("unbounded explicit public mode was accepted")
	}
	if err := validateBotAccessConfig(map[int64]struct{}{7: {}}, true, 30, 120, 10_000, 100); err == nil {
		t.Fatal("ambiguous allowlist plus public mode was accepted")
	}
}

func TestPublicModeEnforcesGlobalRateLimit(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	d := &botData{
		allowPublic:        true,
		rateLimitPerMinute: 10,
		globalRateLimit:    2,
		rateWindows:        make(map[int64]requestWindow),
	}
	for userID := int64(1); userID <= 2; userID++ {
		if _, decision := d.authorizeUpdate(privateTestUpdate(userID, userID, models.ChatTypePrivate), now); decision != accessAllowed {
			t.Fatalf("global request %d decision = %v", userID, decision)
		}
	}
	if message, decision := d.authorizeUpdate(privateTestUpdate(3, 3, models.ChatTypePrivate), now); decision != accessRateLimited || message != nil {
		t.Fatalf("global overflow: decision=%v message=%#v", decision, message)
	}
	if _, decision := d.authorizeUpdate(privateTestUpdate(3, 3, models.ChatTypePrivate), now.Add(time.Minute)); decision != accessAllowed {
		t.Fatalf("global window did not reset: %v", decision)
	}
}

func TestParseAllowedUserIDsAndLimits(t *testing.T) {
	allowed, err := parseAllowedUserIDs(" 7, 42,7 ")
	if err != nil || len(allowed) != 2 {
		t.Fatalf("allowlist parse: allowed=%v err=%v", allowed, err)
	}
	for _, invalid := range []string{"0", "-1", "abc", "7,"} {
		if _, err := parseAllowedUserIDs(invalid); err == nil {
			t.Errorf("invalid allowlist %q was accepted", invalid)
		}
	}
	t.Setenv("MEM_BOT_TEST_LIMIT", "17")
	if got, err := parseNonNegativeLimitEnv("MEM_BOT_TEST_LIMIT", 3); err != nil || got != 17 {
		t.Fatalf("limit parse: got=%d err=%v", got, err)
	}
	t.Setenv("MEM_BOT_TEST_LIMIT", "-1")
	if _, err := parseNonNegativeLimitEnv("MEM_BOT_TEST_LIMIT", 3); err == nil {
		t.Fatal("negative limit was accepted")
	}
	t.Setenv("MEM_BOT_TEST_BOOL", "true")
	if got, err := parseBoolEnv("MEM_BOT_TEST_BOOL", false); err != nil || !got {
		t.Fatalf("bool parse: got=%v err=%v", got, err)
	}
	t.Setenv("MEM_BOT_TEST_BOOL", "sometimes")
	if _, err := parseBoolEnv("MEM_BOT_TEST_BOOL", false); err == nil {
		t.Fatal("invalid bool was accepted")
	}
}

func TestEntryQuotaReached(t *testing.T) {
	d := &botData{dataDir: t.TempDir(), stores: make(map[int64]*userStore), maxEntriesPerUser: 1}
	store, err := d.getOrCreateStore(7)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.closeStores() })
	if reached, err := d.entryQuotaReached(store); err != nil || reached {
		t.Fatal("empty store reached quota")
	}
	if _, err := store.store.Add("entry", "", nil, "test", []float32{1}, false); err != nil {
		t.Fatal(err)
	}
	if reached, err := d.entryQuotaReached(store); err != nil || !reached {
		t.Fatal("full store did not reach quota")
	}
}

func TestEntryQuotaCheckFailsClosedOnDatabaseError(t *testing.T) {
	d := &botData{dataDir: t.TempDir(), stores: make(map[int64]*userStore), maxEntriesPerUser: 1}
	store, err := d.getOrCreateStore(7)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.store.Close(); err != nil {
		t.Fatal(err)
	}
	reached, err := d.entryQuotaReached(store)
	if err == nil || !reached {
		t.Fatalf("quota check on closed database = reached %v, err %v; want fail-closed", reached, err)
	}
}

func TestGetOrCreateStoreEnforcesPersistentUserLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "7"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := &botData{dataDir: root, stores: make(map[int64]*userStore), maxUsers: 1}
	if _, err := d.getOrCreateStore(8); err == nil || !strings.Contains(err.Error(), "лимит пользователей") {
		t.Fatalf("new user beyond limit error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "8")); !os.IsNotExist(err) {
		t.Fatalf("rejected user directory was created: %v", err)
	}
}

func TestGetOrCreateStoreConcurrentSameUserReturnsBusyWithoutDuplicateOpen(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	opened := &userStore{}
	d := &botData{
		stores: make(map[int64]*userStore),
		openUserStore: func(int64) (*userStore, error) {
			close(started)
			<-release
			return opened, nil
		},
	}

	creatorDone := make(chan error, 1)
	go func() {
		store, err := d.getOrCreateStore(42)
		if err == nil && store != opened {
			err = fmt.Errorf("opened store = %p, want %p", store, opened)
		}
		creatorDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("store opener did not start")
	}

	for range 8 {
		if _, err := d.getOrCreateStore(42); !errors.Is(err, errStoreLoadBusy) {
			t.Fatalf("concurrent same-user open error = %v, want busy", err)
		}
	}
	unblock()
	select {
	case err := <-creatorDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("store load did not finish")
	}
	if cached, err := d.getOrCreateStore(42); err != nil || cached != opened {
		t.Fatalf("cached store = %p, err %v; want %p", cached, err, opened)
	}
}

func TestStoreOpenContentionLeavesWorkerForLightweightCommand(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	opened := &userStore{}
	d := &botData{
		stores:       make(map[int64]*userStore),
		allowedUsers: map[int64]struct{}{99: {}},
		openUserStore: func(userID int64) (*userStore, error) {
			if userID != 7 {
				return nil, fmt.Errorf("unexpected opener user %d", userID)
			}
			close(started)
			<-release
			return opened, nil
		},
	}

	jobs := make(chan func(), botWorkers+1)
	var workers sync.WaitGroup
	for range botWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				job()
			}
		}()
	}
	defer func() {
		unblock()
		close(jobs)
		workers.Wait()
	}()

	creatorDone := make(chan error, 1)
	jobs <- func() {
		_, err := d.getOrCreateStore(7)
		creatorDone <- err
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("store opener did not start")
	}

	busyDone := make(chan error, botWorkers-1)
	for userID := int64(8); userID < 8+botWorkers-1; userID++ {
		userID := userID
		jobs <- func() {
			_, err := d.getOrCreateStore(userID)
			if !errors.Is(err, errStoreLoadBusy) {
				busyDone <- fmt.Errorf("user %d contention error = %v, want busy", userID, err)
				return
			}
			busyDone <- nil
		}
	}
	lightweightDone := make(chan accessDecision, 1)
	jobs <- func() {
		_, decision := d.authorizeUpdate(privateTestUpdate(99, 99, models.ChatTypePrivate), time.Now())
		lightweightDone <- decision
	}
	select {
	case decision := <-lightweightDone:
		if decision != accessAllowed {
			t.Fatalf("lightweight authorization = %v", decision)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("four concurrent store opens exhausted all Telegram workers")
	}
	for range botWorkers - 1 {
		if err := <-busyDone; err != nil {
			t.Fatal(err)
		}
	}
	unblock()
	if err := <-creatorDone; err != nil {
		t.Fatal(err)
	}
}

func TestGetOrCreateStoreIOLeavesAuthorizationAvailable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	opened := &userStore{}
	d := &botData{
		stores:       make(map[int64]*userStore),
		allowedUsers: map[int64]struct{}{7: {}, 8: {}},
		openUserStore: func(userID int64) (*userStore, error) {
			if userID != 7 {
				return nil, fmt.Errorf("unexpected opener user %d", userID)
			}
			close(started)
			<-release
			return opened, nil
		},
	}

	loadDone := make(chan error, 1)
	go func() {
		store, err := d.getOrCreateStore(7)
		if err == nil && store != opened {
			err = fmt.Errorf("opened store = %p, want %p", store, opened)
		}
		loadDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("store opener did not start")
	}

	authorized := make(chan accessDecision, 1)
	go func() {
		_, decision := d.authorizeUpdate(privateTestUpdate(8, 8, models.ChatTypePrivate), time.Now())
		authorized <- decision
	}()
	select {
	case decision := <-authorized:
		if decision != accessAllowed {
			t.Fatalf("authorization decision while other store opens = %v", decision)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("store I/O held the shared admission mutex")
	}

	unblock()
	select {
	case err := <-loadDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("store load did not finish")
	}
}

func TestGetOrCreateStoreRejectsCorruptConfig(t *testing.T) {
	root := t.TempDir()
	memDir := filepath.Join(root, "7", mem.MemDirName)
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mem.ConfigPathIn(memDir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := &botData{dataDir: root, stores: make(map[int64]*userStore)}
	_, err := d.getOrCreateStore(7)
	if err == nil || !strings.Contains(err.Error(), "config:") {
		t.Fatalf("error = %v, want actionable config error", err)
	}
}

func TestCloseStoresReleasesSQLiteFile(t *testing.T) {
	d := &botData{dataDir: t.TempDir(), stores: make(map[int64]*userStore)}
	store, err := d.getOrCreateStore(9)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(store.dir, "store.db")

	if err := d.closeStores(); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	cacheSize := len(d.stores)
	d.mu.Unlock()
	if cacheSize != 0 {
		t.Fatalf("cache size after close = %d, want 0", cacheSize)
	}

	movedPath := dbPath + ".moved"
	if err := os.Rename(dbPath, movedPath); err != nil {
		t.Fatalf("SQLite file is still held after close: %v", err)
	}
	if err := os.Rename(movedPath, dbPath); err != nil {
		t.Fatalf("restore SQLite file: %v", err)
	}
}

func TestTruncatePreservesUnicode(t *testing.T) {
	if got := truncate("абвг", 3); got != "абв..." {
		t.Fatalf("truncate = %q, want %q", got, "абв...")
	}
	if got := truncate("абв", 0); got != "" {
		t.Fatalf("truncate with zero limit = %q, want empty", got)
	}

	message := strings.Repeat("🙂", 4100)
	got := truncateMessage(message)
	if !utf8.ValidString(got) {
		t.Fatal("truncateMessage returned invalid UTF-8")
	}
	if runes := len([]rune(got)); runes != 4000 {
		t.Fatalf("truncateMessage rune count = %d, want 4000", runes)
	}
	if !strings.HasSuffix(got, "\n\n... _(обрезано)_") {
		t.Fatalf("truncateMessage suffix missing: %q", got[len(got)-40:])
	}
}

func TestSendMessageDoesNotRetryNonParseFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "rate limit", err: &bot.TooManyRequestsError{Message: "too many requests", RetryAfter: 30}},
		{name: "canceled", err: context.Canceled},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "transport", err: errors.New("connection reset")},
		{name: "unconfirmed parse text", err: errors.New("bad request: can't parse entities")},
		{name: "other HTTP 400", err: fmt.Errorf("%w: chat not found", bot.ErrorBadRequest)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender := &fakeTelegramSender{errors: []error{tc.err}}
			got := sendMessage(context.Background(), sender, 7, "message")
			if !errors.Is(got, tc.err) {
				t.Fatalf("sendMessage error = %v, want %v", got, tc.err)
			}
			if calls := len(sender.params); calls != 1 {
				t.Fatalf("SendMessage calls = %d, want 1", calls)
			}
		})
	}
}

func TestSendMessageRetriesParseEntitiesErrorExactlyOnce(t *testing.T) {
	parseErr := fmt.Errorf("%w: Bad Request: can't parse entities: can't find end of the entity", bot.ErrorBadRequest)
	sender := &fakeTelegramSender{errors: []error{parseErr, nil}}
	if err := sendMessage(context.Background(), sender, 7, "**broken"); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if calls := len(sender.params); calls != 2 {
		t.Fatalf("SendMessage calls = %d, want 2", calls)
	}
	if sender.params[0].ParseMode != models.ParseModeMarkdown {
		t.Fatalf("first parse mode = %q, want Markdown", sender.params[0].ParseMode)
	}
	if sender.params[1].ParseMode != "" {
		t.Fatalf("fallback parse mode = %q, want empty", sender.params[1].ParseMode)
	}
}

func TestSendMessageSurfacesFallbackFailure(t *testing.T) {
	parseErr := fmt.Errorf("%w: Bad Request: can't parse entities", bot.ErrorBadRequest)
	fallbackErr := errors.New("fallback transport failed")
	sender := &fakeTelegramSender{errors: []error{parseErr, fallbackErr}}
	err := sendMessage(context.Background(), sender, 7, "**broken")
	if !errors.Is(err, fallbackErr) {
		t.Fatalf("sendMessage error = %v, want fallback error", err)
	}
	if calls := len(sender.params); calls != 2 {
		t.Fatalf("SendMessage calls = %d, want 2", calls)
	}
}
