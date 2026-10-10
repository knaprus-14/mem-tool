package main

import "github.com/knaprus-14/mem-tool/internal/applog"

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	mem "github.com/knaprus-14/mem-tool/pkg/mem"
)

func parseConfigCommandArgs(args []string) ([]string, error) {
	args = commandPositionals(args)
	if len(args) == 0 {
		return args, nil
	}

	usage := ""
	switch args[0] {
	case "set-backend":
		usage = "mem config set-backend <ollama|polza>"
	case "set-polza-key":
		usage = "mem config set-polza-key <api_key>"
	case "set-polza-model":
		usage = "mem config set-polza-model <model_name>"
	case "set-ollama-model":
		usage = "mem config set-ollama-model <model_name>"
	case "set-answer-model":
		usage = "mem config set-answer-model <model_name>"
	case "set-answer-base-url":
		usage = "mem config set-answer-base-url <local-url>"
	case "set-answer-timeout":
		usage = "mem config set-answer-timeout <секунды>"
	case "set-answer-max-tokens":
		usage = "mem config set-answer-max-tokens <число>"
	case "set-answer-context-chars":
		usage = "mem config set-answer-context-chars <число>"
	case "set-chunk-size":
		usage = "mem config set-chunk-size <символов>"
	case "set-chunk-overlap":
		usage = "mem config set-chunk-overlap <символов>"
	case "set-chunk-strategy":
		usage = "mem config set-chunk-strategy <paragraph|sentence|fixed>"
	default:
		return nil, fmt.Errorf("неизвестная команда: %s", args[0])
	}
	if len(args) != 2 || (args[0] == "set-answer-model" && strings.TrimSpace(args[1]) == "") {
		return nil, fmt.Errorf("использование: %s", usage)
	}
	return args, nil
}

func handleConfig(args []string) (applogResult0 error) {
	defer applog.Track("cmd.mem.handleConfig")(&applogResult0)

	parsedArgs, err := parseConfigCommandArgs(args)
	if err != nil {
		return err
	}
	args = parsedArgs

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		displayConfig := *cfg
		if displayConfig.Polza.APIKey != "" {
			displayConfig.Polza.APIKey = "[скрыт]"
		}
		data, _ := json.MarshalIndent(&displayConfig, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	cmd := args[0]
	switch cmd {
	case "set-backend":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-backend <ollama|polza>")
		}
		backend := args[1]
		if backend != "ollama" && backend != "polza" {
			return fmt.Errorf("бэкенд должен быть 'ollama' или 'polza'")
		}
		cfg.Backend = backend
		return saveConfig(cfg)

	case "set-polza-key":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-polza-key <api_key>")
		}
		cfg.Polza.APIKey = args[1]
		return saveConfig(cfg)

	case "set-polza-model":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-polza-model <model_name>")
		}
		cfg.Polza.Model = args[1]
		return saveConfig(cfg)

	case "set-ollama-model":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-ollama-model <model_name>")
		}
		cfg.Ollama.Model = args[1]
		return saveConfig(cfg)

	case "set-answer-model":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return fmt.Errorf("использование: mem config set-answer-model <model_name>")
		}
		candidate := cfg.Answer
		candidate.Model = strings.TrimSpace(args[1])
		if _, err := mem.NewOllamaAnswerProvider(candidate); err != nil {
			return err
		}
		cfg.Answer.Model = candidate.Model
		return saveConfig(cfg)

	case "set-answer-base-url":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-answer-base-url <local-url>")
		}
		baseURL, err := mem.NormalizeLocalAnswerBaseURL(args[1])
		if err != nil {
			return err
		}
		cfg.Answer.BaseURL = baseURL
		return saveConfig(cfg)

	case "set-answer-timeout":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-answer-timeout <секунды>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 || n > mem.MaxAnswerTimeoutSeconds {
			return fmt.Errorf("таймаут ответа должен быть от 1 до %d секунд", mem.MaxAnswerTimeoutSeconds)
		}
		cfg.Answer.TimeoutSeconds = n
		return saveConfig(cfg)

	case "set-answer-max-tokens":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-answer-max-tokens <число>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 || n > mem.MaxAnswerTokens {
			return fmt.Errorf("максимум токенов ответа должен быть от 1 до %d", mem.MaxAnswerTokens)
		}
		cfg.Answer.MaxTokens = n
		return saveConfig(cfg)

	case "set-answer-context-chars":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-answer-context-chars <число>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n <= 0 || n > mem.MaxAnswerContextChars {
			return fmt.Errorf("бюджет evidence должен быть от 1 до %d символов", mem.MaxAnswerContextChars)
		}
		cfg.Answer.ContextChars = n
		return saveConfig(cfg)

	case "set-chunk-size":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-chunk-size <символов>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 100 || n > mem.MaxEmbeddingChars {
			return fmt.Errorf("размер чанка должен быть от 100 до %d", mem.MaxEmbeddingChars)
		}
		cfg.Chunking.MaxSize = n
		return saveConfig(cfg)

	case "set-chunk-overlap":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-chunk-overlap <символов>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n > 1000 {
			return fmt.Errorf("перекрытие должно быть от 0 до 1000")
		}
		cfg.Chunking.Overlap = n
		return saveConfig(cfg)

	case "set-chunk-strategy":
		if len(args) != 2 {
			return fmt.Errorf("использование: mem config set-chunk-strategy <paragraph|sentence|fixed>")
		}
		strategy := args[1]
		if strategy != "paragraph" && strategy != "sentence" && strategy != "fixed" {
			return fmt.Errorf("стратегия должна быть: paragraph, sentence или fixed")
		}
		cfg.Chunking.Strategy = strategy
		return saveConfig(cfg)

	default:
		return fmt.Errorf("неизвестная команда: %s", cmd)
	}
}
