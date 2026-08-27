package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/knaprus-14/mem-tool/internal/buildinfo"
	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

const (
	// dataDir — корневая директория для всех данных бота
	defaultDataDir = "./data"
	// botWorkers ограничивает параллелизм и позволяет Start дождаться активных
	// handler-ов перед закрытием пользовательских SQLite-баз.
	botWorkers = 4
	// Embedding requests are deliberately capped below botWorkers so that
	// lightweight commands keep at least half of the update workers available.
	// A single user may have only one such request in flight; this check happens
	// before a write can wait on userStore.writeMu.
	botExpensiveWorkers = 2
	botExpensivePerUser = 1

	// Optional hardening controls:
	//   MEM_BOT_ALLOWED_USER_IDS       comma-separated Telegram user IDs; secure default requires this
	//   MEM_BOT_ALLOW_PUBLIC           explicit true enables public private-chat admission
	//   MEM_BOT_RATE_LIMIT_PER_MINUTE  per-user request limit; 0 disables the limiter
	//   MEM_BOT_GLOBAL_RATE_LIMIT_PER_MINUTE global request limit; 0 allowed only with an allowlist
	//   MEM_BOT_MAX_ENTRIES_PER_USER   per-user stored-entry quota; 0 disables the quota
	//   MEM_BOT_MAX_USERS              persistent user-directory limit; 0 disables the limit
	defaultRateLimitPerMinute = 30
	defaultGlobalRateLimit    = 120
	defaultMaxEntriesPerUser  = 10_000
	defaultMaxUsers           = 100
	maxRateWindowEntries      = 4096
)

// userStore — кеш *mem.Store per user_id (чтобы не открывать SQLite на каждое сообщение)
type userStore struct {
	dir     string
	store   *mem.Store
	cfg     *mem.Config
	writeMu sync.Mutex
}

type userStoreLoad struct {
	done chan struct{}
}

type requestWindow struct {
	started  time.Time
	count    int
	notified bool
}

type accessDecision uint8

const (
	accessAllowed accessDecision = iota
	accessInvalidUpdate
	accessNonPrivateChat
	accessUnauthorized
	accessRateLimited
)

// botData — глобальное состояние бота
type botData struct {
	dataDir            string
	mu                 sync.Mutex
	stores             map[int64]*userStore // user_id → userStore
	storeLoads         map[int64]*userStoreLoad
	closing            bool
	storeCreateMu      sync.Mutex
	openUserStore      func(int64) (*userStore, error)
	allowedUsers       map[int64]struct{}
	allowPublic        bool
	rateLimitPerMinute int
	globalRateLimit    int
	maxEntriesPerUser  int
	maxUsers           int
	rateWindows        map[int64]requestWindow
	globalWindow       requestWindow
	expensiveMu        sync.Mutex
	expensiveActive    int
	expensiveByUser    map[int64]int
	expensiveLimit     int
	expensiveUserLimit int
}

var (
	data             *botData
	errStoreLoadBusy = errors.New("личная база уже открывается; повторите через несколько секунд")
)

func main() {
	if isVersion, err := parseBotVersionCommand(os.Args[1:]); isVersion {
		if err != nil {
			fmt.Fprintf(os.Stderr, "ошибка: %v\n", err)
			os.Exit(1)
		}
		mem.PrintVersion("mem-bot", buildinfo.Version)
		return
	}

	// === Конфигурация ===
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN не задан. Получите токен у @BotFather и экспортируйте:\n  $env:TELEGRAM_BOT_TOKEN=\"123456:ABC...\"")
	}

	dataDir := os.Getenv("MEM_BOT_DATA_DIR")
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	allowedUsers, err := parseAllowedUserIDs(os.Getenv("MEM_BOT_ALLOWED_USER_IDS"))
	if err != nil {
		log.Fatalf("MEM_BOT_ALLOWED_USER_IDS: %v", err)
	}
	allowPublic, err := parseBoolEnv("MEM_BOT_ALLOW_PUBLIC", false)
	if err != nil {
		log.Fatal(err)
	}
	rateLimit, err := parseNonNegativeLimitEnv("MEM_BOT_RATE_LIMIT_PER_MINUTE", defaultRateLimitPerMinute)
	if err != nil {
		log.Fatal(err)
	}
	globalRateLimit, err := parseNonNegativeLimitEnv("MEM_BOT_GLOBAL_RATE_LIMIT_PER_MINUTE", defaultGlobalRateLimit)
	if err != nil {
		log.Fatal(err)
	}
	maxEntries, err := parseNonNegativeLimitEnv("MEM_BOT_MAX_ENTRIES_PER_USER", defaultMaxEntriesPerUser)
	if err != nil {
		log.Fatal(err)
	}
	maxUsers, err := parseNonNegativeLimitEnv("MEM_BOT_MAX_USERS", defaultMaxUsers)
	if err != nil {
		log.Fatal(err)
	}
	if err := validateBotAccessConfig(allowedUsers, allowPublic, rateLimit, globalRateLimit, maxEntries, maxUsers); err != nil {
		log.Fatal(err)
	}

	// Создаём корневую директорию
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatalf("не удалось создать %s: %v", dataDir, err)
	}

	data = &botData{
		dataDir:            dataDir,
		stores:             make(map[int64]*userStore),
		allowedUsers:       allowedUsers,
		allowPublic:        allowPublic,
		rateLimitPerMinute: rateLimit,
		globalRateLimit:    globalRateLimit,
		maxEntriesPerUser:  maxEntries,
		maxUsers:           maxUsers,
		rateWindows:        make(map[int64]requestWindow),
	}
	defer func() {
		if err := data.closeStores(); err != nil {
			log.Printf("ошибка закрытия пользовательских баз: %v", err)
		}
	}()

	// === Telegram bot ===
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	opts := []bot.Option{
		bot.WithDefaultHandler(guardedHandler(expensiveHandler(defaultHandler))),
		bot.WithCallbackQueryDataHandler("noop", bot.MatchTypeExact, noopCallback),
		bot.WithWorkers(botWorkers),
		bot.WithNotAsyncHandlers(),
	}

	b, err := bot.New(token, opts...)
	if err != nil {
		log.Fatalf("не удалось создать бота: %v", err)
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, guardedHandler(cmdStart))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/help", bot.MatchTypeExact, guardedHandler(cmdHelp))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/add", bot.MatchTypePrefix, guardedHandler(expensiveHandler(cmdAdd)))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/search", bot.MatchTypePrefix, guardedHandler(expensiveHandler(cmdSearch)))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/recent", bot.MatchTypeExact, guardedHandler(cmdRecent))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stats", bot.MatchTypeExact, guardedHandler(cmdStats))
	b.RegisterHandler(bot.HandlerTypeMessageText, "/import", bot.MatchTypeExact, guardedHandler(cmdImport))

	if allowPublic {
		log.Printf("[mem-bot] ВНИМАНИЕ: явно включён публичный private-chat режим")
	} else {
		log.Printf("[mem-bot] allowlist активен: %d пользователь(ей)", len(allowedUsers))
	}
	log.Printf("[mem-bot v%s] запущен, data dir: %s, rate=%d/min/user, global-rate=%d/min, quota=%d entries/user, max-users=%d", buildinfo.Version, dataDir, rateLimit, globalRateLimit, maxEntries, maxUsers)

	// Запускаем polling в отдельной горутине
	b.Start(ctx)
}

func parseBotVersionCommand(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "version", "--version", "-v":
		if len(args) != 1 {
			return true, errors.New("команда version не принимает аргументы")
		}
		return true, nil
	default:
		return false, nil
	}
}

// === Per-user Store ===

// getOrCreateStore возвращает (или создаёт) Store для пользователя
func (d *botData) getOrCreateStore(userID int64) (*userStore, error) {
	if d == nil || userID <= 0 {
		return nil, fmt.Errorf("некорректный Telegram user ID")
	}

	d.mu.Lock()
	if d.closing {
		d.mu.Unlock()
		return nil, fmt.Errorf("хранилища бота уже закрываются")
	}
	if d.stores == nil {
		d.stores = make(map[int64]*userStore)
	}
	if us, ok := d.stores[userID]; ok {
		d.mu.Unlock()
		return us, nil
	}
	if d.storeLoads == nil {
		d.storeLoads = make(map[int64]*userStoreLoad)
	}
	if _, ok := d.storeLoads[userID]; ok {
		d.mu.Unlock()
		return nil, errStoreLoadBusy
	}
	pending := &userStoreLoad{done: make(chan struct{})}
	d.storeLoads[userID] = pending
	opener := d.openUserStore
	d.mu.Unlock()
	if !d.storeCreateMu.TryLock() {
		d.mu.Lock()
		delete(d.storeLoads, userID)
		close(pending.done)
		d.mu.Unlock()
		return nil, errStoreLoadBusy
	}
	defer d.storeCreateMu.Unlock()

	var (
		us  *userStore
		err error
	)
	if opener != nil {
		us, err = opener(userID)
	} else {
		us, err = d.loadUserStore(userID)
	}

	d.mu.Lock()
	if err == nil {
		d.stores[userID] = us
	}
	delete(d.storeLoads, userID)
	close(pending.done)
	d.mu.Unlock()
	return us, err
}

// loadUserStore performs filesystem/config/SQLite I/O without holding d.mu.
// getOrCreateStore serializes calls with a non-blocking creation gate, preserving
// the persistent max-users invariant without occupying all Telegram workers.
func (d *botData) loadUserStore(userID int64) (*userStore, error) {
	// Каждый юзер — в своей подпапке: data/<user_id>/
	userDir := filepath.Join(d.dataDir, strconv.FormatInt(userID, 10))
	if d.maxUsers > 0 {
		if _, err := os.Stat(userDir); os.IsNotExist(err) {
			count, countErr := countBotUserDirs(d.dataDir)
			if countErr != nil {
				return nil, fmt.Errorf("проверка лимита пользователей: %w", countErr)
			}
			if count >= d.maxUsers {
				return nil, fmt.Errorf("достигнут лимит пользователей бота (%d); настройте MEM_BOT_ALLOWED_USER_IDS или MEM_BOT_MAX_USERS", d.maxUsers)
			}
		}
	}
	memDir := filepath.Join(userDir, mem.MemDirName)

	// Создаём .mem/ если её нет
	if !mem.MemExistsIn(memDir) {
		name := fmt.Sprintf("tg-%d", userID)
		if err := mem.InitMemIn(memDir, name); err != nil {
			return nil, fmt.Errorf("init: %w", err)
		}
	}

	// Конфиг
	cfg, err := mem.LoadConfigIn(memDir)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	// Store
	store, err := mem.NewStore(memDir)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	us := &userStore{dir: memDir, store: store, cfg: cfg}
	return us, nil
}

func countBotUserDirs(root string) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, err := strconv.ParseInt(entry.Name(), 10, 64)
		if err == nil && id > 0 {
			count++
		}
	}
	return count, nil
}

// closeStores атомарно отсоединяет кеш пользовательских баз и закрывает все
// SQLite-соединения после остановки polling. Новые handler-ы после отмены
// root-context больше не должны запускаться.
func (d *botData) closeStores() error {
	if d == nil {
		return nil
	}

	d.mu.Lock()
	d.closing = true
	pendingLoads := make([]<-chan struct{}, 0, len(d.storeLoads))
	for _, pending := range d.storeLoads {
		pendingLoads = append(pendingLoads, pending.done)
	}
	d.mu.Unlock()
	for _, done := range pendingLoads {
		<-done
	}

	d.mu.Lock()
	stores := d.stores
	d.stores = make(map[int64]*userStore)
	d.mu.Unlock()

	var closeErrors []error
	for userID, us := range stores {
		if us == nil || us.store == nil {
			continue
		}
		if err := us.store.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("user %d: %w", userID, err))
		}
	}
	return errors.Join(closeErrors...)
}

func parseAllowedUserIDs(raw string) (map[int64]struct{}, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	allowed := make(map[int64]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q не является положительным Telegram user ID", part)
		}
		allowed[id] = struct{}{}
	}
	return allowed, nil
}

func parseNonNegativeLimitEnv(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s должен быть целым числом не меньше нуля", name)
	}
	return value, nil
}

func parseBoolEnv(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s должен быть true или false", name)
	}
	return value, nil
}

func validateBotAccessConfig(allowed map[int64]struct{}, allowPublic bool, perUserRate, globalRate, maxEntries, maxUsers int) error {
	if len(allowed) == 0 && !allowPublic {
		return errors.New("доступ mem-bot закрыт: задайте MEM_BOT_ALLOWED_USER_IDS или явно включите MEM_BOT_ALLOW_PUBLIC=true")
	}
	if len(allowed) > 0 && allowPublic {
		return errors.New("MEM_BOT_ALLOWED_USER_IDS и MEM_BOT_ALLOW_PUBLIC=true нельзя использовать вместе")
	}
	if allowPublic && (perUserRate <= 0 || globalRate <= 0 || maxEntries <= 0 || maxUsers <= 0) {
		return errors.New("публичный режим требует положительные MEM_BOT_RATE_LIMIT_PER_MINUTE, MEM_BOT_GLOBAL_RATE_LIMIT_PER_MINUTE, MEM_BOT_MAX_ENTRIES_PER_USER и MEM_BOT_MAX_USERS")
	}
	return nil
}

func privateMessage(update *models.Update) (*models.Message, bool) {
	if update == nil || update.Message == nil || update.Message.From == nil || update.Message.From.ID <= 0 || update.Message.Chat.Type != models.ChatTypePrivate {
		return nil, false
	}
	return update.Message, true
}

// authorizeUpdate is deliberately evaluated before any Store is opened. This
// prevents group chats from reading a user's private store and lets a personal
// bot reject unknown or abusive callers without spending embedding/API quota.
func (d *botData) authorizeUpdate(update *models.Update, now time.Time) (*models.Message, accessDecision) {
	message, ok := privateMessage(update)
	if !ok {
		if update != nil && update.Message != nil && update.Message.Chat.Type != models.ChatTypePrivate {
			return nil, accessNonPrivateChat
		}
		return nil, accessInvalidUpdate
	}
	if d == nil {
		return nil, accessUnauthorized
	}

	userID := message.From.ID
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.allowedUsers) > 0 {
		if _, allowed := d.allowedUsers[userID]; !allowed {
			return nil, accessUnauthorized
		}
	} else if !d.allowPublic {
		return nil, accessUnauthorized
	}
	if d.rateLimitPerMinute <= 0 {
		if !d.admitGlobalLocked(now) {
			return nil, accessRateLimited
		}
		return message, accessAllowed
	}
	if d.rateWindows == nil {
		d.rateWindows = make(map[int64]requestWindow)
	}
	if _, exists := d.rateWindows[userID]; !exists && len(d.rateWindows) >= maxRateWindowEntries {
		for id, candidate := range d.rateWindows {
			if candidate.started.IsZero() || now.Before(candidate.started) || now.Sub(candidate.started) >= time.Minute {
				delete(d.rateWindows, id)
			}
		}
		if len(d.rateWindows) >= maxRateWindowEntries {
			return nil, accessUnauthorized
		}
	}
	window := d.rateWindows[userID]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute || now.Before(window.started) {
		window = requestWindow{started: now}
	}
	if window.count >= d.rateLimitPerMinute {
		if window.notified {
			return nil, accessRateLimited
		}
		window.notified = true
		d.rateWindows[userID] = window
		return message, accessRateLimited
	}
	window.count++
	d.rateWindows[userID] = window
	if !d.admitGlobalLocked(now) {
		return nil, accessRateLimited
	}
	return message, accessAllowed
}

// admitGlobalLocked applies the aggregate cost ceiling after per-user
// admission. d.mu must be held by the caller.
func (d *botData) admitGlobalLocked(now time.Time) bool {
	if d.globalRateLimit <= 0 {
		return true
	}
	window := d.globalWindow
	if window.started.IsZero() || now.Before(window.started) || now.Sub(window.started) >= time.Minute {
		window = requestWindow{started: now}
	}
	if window.count >= d.globalRateLimit {
		return false
	}
	window.count++
	d.globalWindow = window
	return true
}

func guardedHandler(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		message, decision := data.authorizeUpdate(update, time.Now())
		if decision == accessRateLimited && message != nil {
			sendMessage(ctx, b, message.Chat.ID, "Слишком много запросов. Повтори через минуту.")
			return
		}
		if decision != accessAllowed {
			return
		}
		next(ctx, b, update)
	}
}

// tryAcquireExpensive admits embedding/model work without waiting. Keeping the
// admission state separate from botData.mu means a long store/config operation
// cannot delay authorization of lightweight commands. The returned release
// function is idempotent and does not leave per-user state behind.
func (d *botData) tryAcquireExpensive(userID int64) (func(), bool) {
	if d == nil || userID <= 0 {
		return nil, false
	}

	d.expensiveMu.Lock()
	globalLimit := d.expensiveLimit
	if globalLimit <= 0 {
		globalLimit = botExpensiveWorkers
	}
	userLimit := d.expensiveUserLimit
	if userLimit <= 0 {
		userLimit = botExpensivePerUser
	}
	if d.expensiveActive >= globalLimit || d.expensiveByUser[userID] >= userLimit {
		d.expensiveMu.Unlock()
		return nil, false
	}
	if d.expensiveByUser == nil {
		d.expensiveByUser = make(map[int64]int)
	}
	d.expensiveActive++
	d.expensiveByUser[userID]++
	d.expensiveMu.Unlock()

	released := false
	return func() {
		d.expensiveMu.Lock()
		defer d.expensiveMu.Unlock()
		if released {
			return
		}
		released = true
		d.expensiveActive--
		if d.expensiveByUser[userID] <= 1 {
			delete(d.expensiveByUser, userID)
		} else {
			d.expensiveByUser[userID]--
		}
	}, true
}

type busyNotifier func(context.Context, *bot.Bot, int64)

func expensiveHandler(next bot.HandlerFunc) bot.HandlerFunc {
	return expensiveHandlerWithNotifier(next, func(ctx context.Context, b *bot.Bot, chatID int64) {
		sendMessage(ctx, b, chatID, "Сервис занят обработкой длинных запросов. Повтори через несколько секунд.")
	})
}

// expensiveHandlerWithNotifier keeps the admission path deterministic and
// testable without a Telegram connection. Rejected work is answered promptly
// from the current worker instead of waiting for another embedding or write.
func expensiveHandlerWithNotifier(next bot.HandlerFunc, notifyBusy busyNotifier) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		message, ok := privateMessage(update)
		if !ok {
			return
		}
		currentData := data
		release, admitted := currentData.tryAcquireExpensive(message.From.ID)
		if !admitted {
			if notifyBusy != nil {
				notifyBusy(ctx, b, message.Chat.ID)
			}
			return
		}
		defer release()
		next(ctx, b, update)
	}
}

func (d *botData) entryQuotaReached(us *userStore) (bool, error) {
	if d == nil || d.maxEntriesPerUser <= 0 {
		return false, nil
	}
	if us == nil || us.store == nil {
		return true, fmt.Errorf("проверка квоты: хранилище не открыто")
	}
	total, err := us.store.EntryCount()
	if err != nil {
		return true, fmt.Errorf("проверка квоты: %w", err)
	}
	return total >= d.maxEntriesPerUser, nil
}

// === Handlers ===

func cmdStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	message, ok := privateMessage(update)
	if !ok {
		return
	}
	user := message.From
	text := fmt.Sprintf(`👋 Привет, %s!

Я — mem-bot, твоя личная база знаний в Telegram.

📝 **Что я умею:**
• Запоминать любые мысли, факты, идеи
• Находить их потом через семантический поиск
• Хранить всё в твоей личной базе на сервере

💡 **Как пользоваться:**
Просто напиши мне любой текст — и я его запомню.

Или используй команды:
/add <текст> — добавить запись
/search <запрос> — найти по смыслу
/recent — последние 5 записей
/stats — статистика базы
/help — эта справка

Версия: mem-bot v%s`, user.FirstName, buildinfo.Version)

	sendMessage(ctx, b, message.Chat.ID, text)
}

func cmdHelp(ctx context.Context, b *bot.Bot, update *models.Update) {
	message, ok := privateMessage(update)
	if !ok {
		return
	}
	sendMessage(ctx, b, message.Chat.ID, helpText())
}

func cmdAdd(ctx context.Context, b *bot.Bot, update *models.Update) {
	message, ok := privateMessage(update)
	if !ok {
		return
	}
	userID := message.From.ID
	text := strings.TrimPrefix(message.Text, "/add")
	text = strings.TrimSpace(text)
	if text == "" {
		sendMessage(ctx, b, message.Chat.ID, "⚠️ Укажи текст после команды:\n`/add Сервер: 157.22.196.67`")
		return
	}

	us, err := data.getOrCreateStore(userID)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "add/open-store", err, "Не удалось открыть личную базу. Попробуй позже.")
		return
	}
	us.writeMu.Lock()
	defer us.writeMu.Unlock()
	quotaReached, err := data.entryQuotaReached(us)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "add/quota", err, "Не удалось проверить лимит личной базы. Попробуй позже.")
		return
	}
	if quotaReached {
		sendMessage(ctx, b, message.Chat.ID, fmt.Sprintf("Лимит личной базы достигнут (%d записей). Удали ненужные записи или увеличь MEM_BOT_MAX_ENTRIES_PER_USER.", data.maxEntriesPerUser))
		return
	}

	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(us.cfg)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "add/embedding-config", err, "Сервис поиска временно недоступен. Попробуй позже.")
		return
	}
	// Эмбеддинг
	emb, err := mem.GetEmbeddingContext(ctx, us.cfg, text)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "add/embedding", err, "Сервис поиска временно недоступен. Попробуй позже.")
		return
	}
	entry, err := us.store.AddWithEmbeddingIdentity(text, "", nil, embeddingIdentity, emb, false)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "add/save", err, "Не удалось сохранить запись. Попробуй позже.")
		return
	}

	msg := fmt.Sprintf("✅ Запись #%d сохранена\n\n📝 %s", entry.ID, truncate(text, 200))
	sendMessage(ctx, b, message.Chat.ID, msg)
}

func cmdSearch(ctx context.Context, b *bot.Bot, update *models.Update) {
	message, ok := privateMessage(update)
	if !ok {
		return
	}
	userID := message.From.ID
	query := strings.TrimPrefix(message.Text, "/search")
	query = strings.TrimSpace(query)
	if query == "" {
		sendMessage(ctx, b, message.Chat.ID, "⚠️ Укажи запрос:\n`/search какой пароль от почты`")
		return
	}

	us, err := data.getOrCreateStore(userID)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "search/open-store", err, "Не удалось открыть личную базу. Попробуй позже.")
		return
	}

	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(us.cfg)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "search/embedding-config", err, "Сервис поиска временно недоступен. Попробуй позже.")
		return
	}
	// Embedding запроса
	emb, err := mem.GetEmbeddingContext(ctx, us.cfg, query)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "search/embedding", err, "Сервис поиска временно недоступен. Попробуй позже.")
		return
	}
	results, err := us.store.SearchInEmbeddingSpace(emb, embeddingIdentity.Backend, embeddingIdentity.SpaceID, 5)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "search/query", err, "Не удалось выполнить поиск. Попробуй позже.")
		return
	}

	if len(results) == 0 {
		sendMessage(ctx, b, message.Chat.ID, "🤷 Ничего не нашлось. Попробуй переформулировать запрос.")
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔍 **Результаты по «%s»:**\n\n", query))
	for _, e := range results {
		score := int(e.Score * 100)
		sb.WriteString(fmt.Sprintf("[%d%%] #%d — %s\n", score, e.ID, truncate(e.Text, 200)))
		sb.WriteString("\n")
	}
	sendMessage(ctx, b, message.Chat.ID, sb.String())
}

func cmdRecent(ctx context.Context, b *bot.Bot, update *models.Update) {
	message, ok := privateMessage(update)
	if !ok {
		return
	}
	userID := message.From.ID

	us, err := data.getOrCreateStore(userID)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "recent/open-store", err, "Не удалось открыть личную базу. Попробуй позже.")
		return
	}

	results, err := us.store.Recent(5)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "recent/query", err, "Не удалось загрузить последние записи. Попробуй позже.")
		return
	}

	if len(results) == 0 {
		sendMessage(ctx, b, message.Chat.ID, "📭 База пуста. Добавь первую запись: `/add Привет, мир!`")
		return
	}

	var sb strings.Builder
	sb.WriteString("📜 **Последние записи:**\n\n")
	for _, e := range results {
		date := e.Created
		if t, err := time.Parse(time.RFC3339, date); err == nil {
			date = t.Format("2006-01-02 15:04")
		}
		sb.WriteString(fmt.Sprintf("🕐 %s — #%d\n%s\n\n", date, e.ID, truncate(e.Text, 250)))
	}
	sendMessage(ctx, b, message.Chat.ID, sb.String())
}

func cmdStats(ctx context.Context, b *bot.Bot, update *models.Update) {
	message, ok := privateMessage(update)
	if !ok {
		return
	}
	userID := message.From.ID

	us, err := data.getOrCreateStore(userID)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "stats/open-store", err, "Не удалось открыть личную базу. Попробуй позже.")
		return
	}

	stats := us.store.Stats()
	msg := fmt.Sprintf("📊 **Статистика базы:**\n\n"+
		"• Всего записей: %v\n"+
		"• Чанков документов: %v\n"+
		"• Бэкенд: %v",
		stats["total_entries"],
		stats["doc_chunks"],
		us.cfg.Backend)
	sendMessage(ctx, b, message.Chat.ID, msg)
}

func cmdImport(ctx context.Context, b *bot.Bot, update *models.Update) {
	message, ok := privateMessage(update)
	if !ok {
		return
	}
	sendMessage(ctx, b, message.Chat.ID, "📎 Пришли мне файл (txt, md, pdf, csv) — я его проиндексирую.\n\n_(пока в разработке)_")
}

func defaultHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	message, ok := privateMessage(update)
	if !ok {
		return
	}
	// Любое сообщение без команды = добавить запись
	userID := message.From.ID
	text := message.Text
	if text == "" {
		return
	}

	us, err := data.getOrCreateStore(userID)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "message/open-store", err, "Не удалось открыть личную базу. Попробуй позже.")
		return
	}
	us.writeMu.Lock()
	defer us.writeMu.Unlock()
	quotaReached, err := data.entryQuotaReached(us)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "message/quota", err, "Не удалось проверить лимит личной базы. Попробуй позже.")
		return
	}
	if quotaReached {
		sendMessage(ctx, b, message.Chat.ID, fmt.Sprintf("Лимит личной базы достигнут (%d записей). Удали ненужные записи или увеличь MEM_BOT_MAX_ENTRIES_PER_USER.", data.maxEntriesPerUser))
		return
	}

	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(us.cfg)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "message/embedding-config", err, "Сервис поиска временно недоступен. Попробуй позже.")
		return
	}
	emb, err := mem.GetEmbeddingContext(ctx, us.cfg, text)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "message/embedding", err, "Сервис поиска временно недоступен. Попробуй позже.")
		return
	}
	entry, err := us.store.AddWithEmbeddingIdentity(text, "", nil, embeddingIdentity, emb, false)
	if err != nil {
		sendOperationError(ctx, b, message.Chat.ID, "message/save", err, "Не удалось сохранить запись. Попробуй позже.")
		return
	}

	msg := fmt.Sprintf("✅ Запомнила (#%d)\n\n📝 %s", entry.ID, truncate(text, 200))
	sendMessage(ctx, b, message.Chat.ID, msg)
}

func noopCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	// заглушка для callback-кнопок
}

// === Helpers ===

func sendOperationError(ctx context.Context, b *bot.Bot, chatID int64, operation string, err error, userMessage string) {
	log.Printf("[mem-bot] %s failed for chat %d: %v", operation, chatID, err)
	sendMessage(ctx, b, chatID, "❌ "+userMessage)
}

type telegramMessageSender interface {
	SendMessage(context.Context, *bot.SendMessageParams) (*models.Message, error)
}

func sendMessage(ctx context.Context, b telegramMessageSender, chatID int64, text string) error {
	// Telegram лимит — 4096 символов на сообщение
	text = truncateMessage(text)
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
	if err == nil {
		return nil
	}
	if !isTelegramParseEntitiesError(err) {
		log.Printf("send error: %v", err)
		return err
	}

	log.Printf("send markdown parse error, retrying without formatting: %v", err)
	_, fallbackErr := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	if fallbackErr != nil {
		combined := fmt.Errorf("markdown send failed (%v); plain-text fallback failed: %w", err, fallbackErr)
		log.Printf("send fallback error: %v", combined)
		return combined
	}
	return nil
}

func isTelegramParseEntitiesError(err error) bool {
	if !errors.Is(err, bot.ErrorBadRequest) {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "can't parse entities")
}

func helpText() string {
	return "📖 **Справка по mem-bot**\n\n" +
		"Бот работает только в личном чате: групповые сообщения не читают и не изменяют твою базу.\n\n" +
		"**Основные команды:**\n" +
		"• `/add <текст>` — сохранить запись\n" +
		"• `/search <запрос>` — найти по смыслу\n" +
		"• `/recent` — последние 5 записей\n" +
		"• `/stats` — статистика\n" +
		"• `/import` — индексировать файл (в разработке)\n\n" +
		"**Совет:** просто напиши любой текст без команды — я его запомню.\n\n" +
		"**Примеры:**\n```\n" +
		"/add Сервер: 157.22.196.67, root\n" +
		"/search где лежит бэкап\n" +
		"/recent\n```\n\n" +
		"Все данные хранятся в твоей личной базе на сервере, в папке `data/<твой_id>/.mem/`."
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func truncateMessage(text string) string {
	const limit = 4000
	const suffix = "\n\n... _(обрезано)_"

	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	suffixRunes := []rune(suffix)
	keep := limit - len(suffixRunes)
	return string(runes[:keep]) + suffix
}
