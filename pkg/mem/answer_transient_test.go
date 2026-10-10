package mem

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestOllamaTransientStatusClassification(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 408, 413, 429, 500, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"test failure"}`)
			}))
			defer server.Close()
			provider, err := NewOllamaAnswerProvider(AnswerConfig{BaseURL: server.URL, Model: "local-chat"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Generate(context.Background(), AnswerRequest{System: "system", Prompt: "prompt"})
			var transient *AnswerTransientError
			got := errors.As(err, &transient)
			want := status == 408 || status == 429 || status == 502 || status == 503 || status == 504
			if err == nil || got != want || calls != 1 {
				t.Fatal(status, err, calls)
			}
			if got && (transient.StatusCode != status || transient.RetryAfter != 7*time.Second) {
				t.Fatal(transient)
			}
		})
	}
}

func TestAnswerRetryAfterAndTransportBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		value string
		want  time.Duration
	}{
		{" 12 ", 12 * time.Second}, {"0", 0}, {"-1", 0}, {"invalid", 0},
		{now.Add(9 * time.Second).Format(http.TimeFormat), 9 * time.Second},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0},
	} {
		if got := parseAnswerRetryAfter(item.value, now); got != item.want {
			t.Fatal(item, got)
		}
	}
	for _, cause := range []error{errors.New("connection reset"), context.Canceled, context.DeadlineExceeded} {
		var transient *AnswerTransientError
		if errors.As(answerTransportFailure(cause), &transient) {
			t.Fatal("retries untyped/cancelled error", cause)
		}
	}
	cause := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
	wrapped := answerTransportFailure(cause)
	var transient *AnswerTransientError
	if !errors.As(wrapped, &transient) || !errors.Is(wrapped, cause) {
		t.Fatal(wrapped)
	}
	if errors.As(answerTransportFailure(&net.OpError{Op: "read", Err: context.DeadlineExceeded}), &transient) {
		t.Fatal("retries expired deadline")
	}
}

type transientAnswerFunc func(context.Context, AnswerRequest) (string, error)

func (f transientAnswerFunc) Generate(ctx context.Context, r AnswerRequest) (string, error) {
	return f(ctx, r)
}

func TestMindMapTransientRetriesAreBoundedAndHonorDelay(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		err                  error
		successAt, wantCalls int
		elapsed              time.Duration
	}{
		{"recovers", &AnswerTransientError{StatusCode: 503}, 3, 3, 3 * time.Second},
		{"exhausted", &AnswerTransientError{StatusCode: 502}, 0, 3, 3 * time.Second},
		{"server_delay", &AnswerTransientError{StatusCode: 429, RetryAfter: 5 * time.Second}, 2, 2, 5 * time.Second},
		{"long_cooldown", &AnswerTransientError{StatusCode: 429, RetryAfter: time.Minute}, 0, 1, 0},
		{"untyped_error", errors.New("temporary provider failure"), 0, 1, 0},
		{"cancelled", context.Canceled, 0, 1, 0},
		{"deadline", context.DeadlineExceeded, 0, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				provider := transientAnswerFunc(func(context.Context, AnswerRequest) (string, error) {
					calls++
					if calls == tc.successAt {
						return `{"nodes":[]}`, nil
					}
					return "", tc.err
				})
				start := time.Now()
				raw, err := generateClassicMindMapAIStrictJSON(context.Background(), provider, AnswerConfig{}, AnswerRequest{MaxTokens: 4096})
				if calls != tc.wantCalls || time.Since(start) != tc.elapsed {
					t.Fatal(calls, time.Since(start))
				}
				if tc.successAt > 0 {
					if err != nil || raw != `{"nodes":[]}` {
						t.Fatal(raw, err)
					}
				} else if !errors.Is(err, tc.err) {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestMindMapTransientWaitCancelsBeforeNextRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		provider := transientAnswerFunc(func(context.Context, AnswerRequest) (string, error) {
			calls++
			return "", &AnswerTransientError{StatusCode: 503, RetryAfter: 10 * time.Second}
		})
		go func() { time.Sleep(time.Second); cancel() }()
		start := time.Now()
		_, err := generateClassicMindMapAIStrictJSON(ctx, provider, AnswerConfig{}, AnswerRequest{MaxTokens: 4096})
		if !errors.Is(err, context.Canceled) || calls != 1 || time.Since(start) != time.Second {
			t.Fatal(err, calls, time.Since(start))
		}
	})
}

func TestSourceMapTransientFailureRecoversWithoutSplitting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir, s, in := sourceMapFixture(t)
		ctx := context.Background()
		task, err := CreateWebMapFromSourcesIn(ctx, dir, "alice", in)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		provider := transientAnswerFunc(func(context.Context, AnswerRequest) (string, error) {
			calls++
			if calls < 3 {
				return "", &AnswerTransientError{StatusCode: 503}
			}
			return sourceMapResponse, nil
		})
		for i := 0; i < 2; i++ {
			if worked, err := processWebMapAIIn(ctx, dir, "alice", provider); err != nil || !worked {
				t.Fatal(worked, err)
			}
		}
		tasks, err := ListWebMapAIIn(ctx, dir, "alice", task.MapID)
		if err != nil || tasks[0].Status != "applied" || calls != 3 {
			t.Fatal(tasks, err, calls)
		}
		var splits int
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM mind_map_generation_parts WHERE run_id=? AND status LIKE 'split%'`, tasks[0].RunID).Scan(&splits); err != nil || splits != 0 {
			t.Fatal(splits, err)
		}
	})
}

func TestWebMapTransientFailureMessage(t *testing.T) {
	message := webMapAIFailureMessage(&AnswerTransientError{StatusCode: 429}, 2, 3)
	if !strings.Contains(message, "продолжите с сохранённого места") {
		t.Fatal(message)
	}
}
