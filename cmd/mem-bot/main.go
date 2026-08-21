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
)

// userStore — кеш *mem.Store per user_id (чтобы не открывать SQLite на каждое сообщение)
type userStore struct {
	dir   string
	store *mem.Store
	cfg   *mem.Config
}

// botData — глобальное состояние бота
type botData struct {
	dataDir string
	mu      sync.Mutex
	stores  map[int64]*userStore // user_id → userStore
}

var data *botData

func main() {
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			mem.PrintVersion("mem-bot", buildinfo.Version)
			return
		}
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

	// Создаём корневую директорию
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatalf("не удалось создать %s: %v", dataDir, err)
	}

	data = &botData{
		dataDir: dataDir,
		stores:  make(map[int64]*userStore),
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
		bot.WithDefaultHandler(defaultHandler),
		bot.WithCallbackQueryDataHandler("noop", bot.MatchTypeExact, noopCallback),
		bot.WithWorkers(botWorkers),
		bot.WithNotAsyncHandlers(),
	}

	b, err := bot.New(token, opts...)
	if err != nil {
		log.Fatalf("не удалось создать бота: %v", err)
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, cmdStart)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/help", bot.MatchTypeExact, cmdHelp)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/add", bot.MatchTypePrefix, cmdAdd)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/search", bot.MatchTypePrefix, cmdSearch)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/recent", bot.MatchTypeExact, cmdRecent)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/stats", bot.MatchTypeExact, cmdStats)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/import", bot.MatchTypeExact, cmdImport)

	log.Printf("[mem-bot v%s] запущен, data dir: %s", buildinfo.Version, dataDir)

	// Запускаем polling в отдельной горутине
	b.Start(ctx)
}

// === Per-user Store ===

// getOrCreateStore возвращает (или создаёт) Store для пользователя
func (d *botData) getOrCreateStore(userID int64) (*userStore, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stores == nil {
		d.stores = make(map[int64]*userStore)
	}

	if us, ok := d.stores[userID]; ok {
		return us, nil
	}

	// Каждый юзер — в своей подпапке: data/<user_id>/
	userDir := filepath.Join(d.dataDir, strconv.FormatInt(userID, 10))
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
	d.stores[userID] = us
	return us, nil
}

// closeStores атомарно отсоединяет кеш пользовательских баз и закрывает все
// SQLite-соединения после остановки polling. Новые handler-ы после отмены
// root-context больше не должны запускаться.
func (d *botData) closeStores() error {
	if d == nil {
		return nil
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

// === Handlers ===

func cmdStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	user := update.Message.From
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

	sendMessage(ctx, b, update.Message.Chat.ID, text)
}

func cmdHelp(ctx context.Context, b *bot.Bot, update *models.Update) {
	sendMessage(ctx, b, update.Message.Chat.ID, helpText())
}

func cmdAdd(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	text := strings.TrimPrefix(update.Message.Text, "/add")
	text = strings.TrimSpace(text)
	if text == "" {
		sendMessage(ctx, b, update.Message.Chat.ID, "⚠️ Укажи текст после команды:\n`/add Сервер: 157.22.196.67`")
		return
	}

	us, err := data.getOrCreateStore(userID)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка инициализации: "+err.Error())
		return
	}

	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(us.cfg)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка конфигурации embedding: "+err.Error())
		return
	}
	// Эмбеддинг
	emb, err := mem.GetEmbeddingContext(ctx, us.cfg, text)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка эмбеддинга: "+err.Error()+"\n\nПроверьте, запущен ли Ollama с моделью bge-m3.")
		return
	}
	entry, err := us.store.AddWithEmbeddingIdentity(text, "", nil, embeddingIdentity, emb, false)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка сохранения: "+err.Error())
		return
	}

	msg := fmt.Sprintf("✅ Запись #%d сохранена\n\n📝 %s", entry.ID, truncate(text, 200))
	sendMessage(ctx, b, update.Message.Chat.ID, msg)
}

func cmdSearch(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID
	query := strings.TrimPrefix(update.Message.Text, "/search")
	query = strings.TrimSpace(query)
	if query == "" {
		sendMessage(ctx, b, update.Message.Chat.ID, "⚠️ Укажи запрос:\n`/search какой пароль от почты`")
		return
	}

	us, err := data.getOrCreateStore(userID)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка: "+err.Error())
		return
	}

	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(us.cfg)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка конфигурации embedding: "+err.Error())
		return
	}
	// Embedding запроса
	emb, err := mem.GetEmbeddingContext(ctx, us.cfg, query)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка эмбеддинга: "+err.Error())
		return
	}
	results, err := us.store.SearchInEmbeddingSpace(emb, embeddingIdentity.Backend, embeddingIdentity.SpaceID, 5)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка поиска: "+err.Error())
		return
	}

	if len(results) == 0 {
		sendMessage(ctx, b, update.Message.Chat.ID, "🤷 Ничего не нашлось. Попробуй переформулировать запрос.")
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔍 **Результаты по «%s»:**\n\n", query))
	for _, e := range results {
		score := int(e.Score * 100)
		sb.WriteString(fmt.Sprintf("[%d%%] #%d — %s\n", score, e.ID, truncate(e.Text, 200)))
		sb.WriteString("\n")
	}
	sendMessage(ctx, b, update.Message.Chat.ID, sb.String())
}

func cmdRecent(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID

	us, err := data.getOrCreateStore(userID)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка: "+err.Error())
		return
	}

	results, err := us.store.Recent(5)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка: "+err.Error())
		return
	}

	if len(results) == 0 {
		sendMessage(ctx, b, update.Message.Chat.ID, "📭 База пуста. Добавь первую запись: `/add Привет, мир!`")
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
	sendMessage(ctx, b, update.Message.Chat.ID, sb.String())
}

func cmdStats(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := update.Message.From.ID

	us, err := data.getOrCreateStore(userID)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка: "+err.Error())
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
	sendMessage(ctx, b, update.Message.Chat.ID, msg)
}

func cmdImport(ctx context.Context, b *bot.Bot, update *models.Update) {
	sendMessage(ctx, b, update.Message.Chat.ID, "📎 Пришли мне файл (txt, md, pdf, csv) — я его проиндексирую.\n\n_(пока в разработке)_")
}

func defaultHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	// Любое сообщение без команды = добавить запись
	userID := update.Message.From.ID
	text := update.Message.Text
	if text == "" {
		return
	}

	us, err := data.getOrCreateStore(userID)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка: "+err.Error())
		return
	}

	embeddingIdentity, err := mem.EmbeddingIdentityForConfig(us.cfg)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка конфигурации embedding: "+err.Error())
		return
	}
	emb, err := mem.GetEmbeddingContext(ctx, us.cfg, text)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка эмбеддинга: "+err.Error())
		return
	}
	entry, err := us.store.AddWithEmbeddingIdentity(text, "", nil, embeddingIdentity, emb, false)
	if err != nil {
		sendMessage(ctx, b, update.Message.Chat.ID, "❌ Ошибка: "+err.Error())
		return
	}

	msg := fmt.Sprintf("✅ Запомнила (#%d)\n\n📝 %s", entry.ID, truncate(text, 200))
	sendMessage(ctx, b, update.Message.Chat.ID, msg)
}

func noopCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	// заглушка для callback-кнопок
}

// === Helpers ===

func sendMessage(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	// Telegram лимит — 4096 символов на сообщение
	text = truncateMessage(text)
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	})
	if err != nil {
		log.Printf("send error: %v", err)
		// Retry без markdown (на случай проблем с разметкой)
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   text,
		})
	}
}

func helpText() string {
	return "📖 **Справка по mem-bot**\n\n" +
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
