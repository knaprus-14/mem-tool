package webapp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/knaprus-14/mem-tool/internal/workspace"
)

type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TokenEnv  string `json:"token_env"`
	tokenHash [32]byte
}
type Config struct {
	MaxWorkspaceBytes int64         `json:"max_workspace_bytes,omitempty"`
	MaxUserBytes      int64         `json:"max_user_bytes,omitempty"`
	Listen            string        `json:"listen"`
	PublicOrigin      string        `json:"public_origin"`
	DataDir           string        `json:"data_dir"`
	Users             []User        `json:"users"`
	Models            []ModelChoice `json:"models,omitempty"`
	Local             bool          `json:"-"`
}

// LoadConfig resolves administrator-provided paths and credentials before any
// listener or database is created. Secret values are never marshalled or logged.
func LoadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return c, err
	}
	if info.Size() > 64<<10 {
		return c, fmt.Errorf("конфигурация сервера превышает 64 KiB")
	}
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, fmt.Errorf("конфигурация сервера: %w", err)
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("после конфигурации обнаружены лишние данные")
	}
	if c.DataDir == "" {
		return c, fmt.Errorf("data_dir обязателен")
	}
	if !filepath.IsAbs(c.DataDir) {
		base, err := filepath.Abs(path)
		if err != nil {
			return c, err
		}
		c.DataDir = filepath.Join(filepath.Dir(base), c.DataDir)
	}
	if err = c.validate(); err != nil {
		return c, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.MaxWorkspaceBytes == 0 {
		c.MaxWorkspaceBytes = 4 << 30
	}
	if c.MaxUserBytes == 0 {
		c.MaxUserBytes = 16 << 30
	}
	if c.MaxWorkspaceBytes < 128<<20 || c.MaxUserBytes < c.MaxWorkspaceBytes {
		return fmt.Errorf("лимиты хранения: база от 128 МиБ; лимит пользователя не меньше лимита базы")
	}
	if len(c.Models) > 100 {
		return fmt.Errorf("models: допускается до 100 моделей")
	}
	seenModels := map[ModelChoice]bool{}
	for _, model := range c.Models {
		if !validModelName(model.Model) || (model.Role != "embedding" && model.Role != "answer") || (model.Backend != "ollama" && model.Backend != "polza") || (model.Role == "answer" && model.Backend != "ollama") || seenModels[model] {
			return fmt.Errorf("models: проверьте роль, подключение и уникальность названий")
		}
		seenModels[model] = true
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8765"
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("listen: ожидается 127.0.0.1:порт")
	}
	ip := net.ParseIP(host)
	n, e := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || e != nil || n < 0 || n > 65535 {
		return fmt.Errorf("listen: разрешён только loopback; внешний доступ настраивается через HTTPS-прокси")
	}
	if c.Local {
		return nil
	}
	u, err := url.Parse(c.PublicOrigin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return fmt.Errorf("public_origin: укажите origin без пути, например https://mem.example.org")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return fmt.Errorf("public_origin требует HTTPS; HTTP разрешён только на loopback для проверки")
	}
	if len(c.Users) == 0 || len(c.Users) > 100 {
		return fmt.Errorf("users: укажите от 1 до 100 разрешённых пользователей")
	}
	ids := map[string]bool{}
	hashes := map[[32]byte]bool{}
	for i := range c.Users {
		user := &c.Users[i]
		if !(workspace.Actor{ID: user.ID}).Valid() || ids[user.ID] {
			return fmt.Errorf("users: некорректный или повторный ID")
		}
		ids[user.ID] = true
		if strings.TrimSpace(user.TokenEnv) == "" {
			return fmt.Errorf("users: token_env обязателен")
		}
		token := os.Getenv(user.TokenEnv)
		if len(token) < 32 {
			return fmt.Errorf("users: секрет входа должен содержать не менее 32 символов; проверьте переменную окружения")
		}
		user.tokenHash = sha256.Sum256([]byte(token))
		if hashes[user.tokenHash] {
			return fmt.Errorf("users: у каждого пользователя должен быть отдельный секрет")
		}
		hashes[user.tokenHash] = true
		if user.Name == "" {
			user.Name = user.ID
		}
	}
	return nil
}
func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}
