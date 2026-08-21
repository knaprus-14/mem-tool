package mem

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// EmbeddingIdentity records the configured vector space that produced an
// embedding. SpaceID includes the backend, model, and sanitized endpoint, but
// exposes none of the endpoint details or credentials in persisted rows.
type EmbeddingIdentity struct {
	Backend string
	Model   string
	SpaceID string
}

// EmbeddingIdentityForConfig returns a stable identity for the configured
// embedding provider. API keys are deliberately excluded. Base URLs containing
// credentials or query parameters are rejected so secrets cannot enter the
// persisted fingerprint input accidentally.
func EmbeddingIdentityForConfig(cfg *Config) (EmbeddingIdentity, error) {
	if cfg == nil {
		return EmbeddingIdentity{}, errors.New("embedding identity: nil config")
	}
	backend := strings.ToLower(strings.TrimSpace(cfg.Backend))
	var model, baseURL string
	switch backend {
	case "ollama":
		model, baseURL = cfg.Ollama.Model, cfg.Ollama.BaseURL
	case "polza":
		model, baseURL = cfg.Polza.Model, cfg.Polza.BaseURL
	default:
		return EmbeddingIdentity{}, fmt.Errorf("embedding identity: unsupported backend %q", cfg.Backend)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return EmbeddingIdentity{}, fmt.Errorf("embedding identity: %s model is empty", backend)
	}
	endpoint, err := normalizedEmbeddingEndpoint(baseURL)
	if err != nil {
		return EmbeddingIdentity{}, fmt.Errorf("embedding identity: %s endpoint: %w", backend, err)
	}
	canonical := "embedding-space:v1\nbackend=" + backend + "\nmodel=" + model + "\nendpoint=" + endpoint
	sum := sha256.Sum256([]byte(canonical))
	return EmbeddingIdentity{
		Backend: backend,
		Model:   model,
		SpaceID: "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

func normalizedEmbeddingEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("base URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("base URL must include scheme and host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("base URL must not contain credentials, query parameters, or a fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func validateEmbeddingIdentity(identity EmbeddingIdentity, backend string) error {
	if identity.Backend != strings.ToLower(strings.TrimSpace(backend)) {
		return fmt.Errorf("embedding identity backend %q does not match entry backend %q", identity.Backend, backend)
	}
	if strings.TrimSpace(identity.Model) == "" {
		return errors.New("embedding identity model is empty")
	}
	if !isSHA256ContentHash(identity.SpaceID) {
		return errors.New("embedding identity space ID is invalid")
	}
	return nil
}
