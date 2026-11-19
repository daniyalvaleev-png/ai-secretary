//go:build eval

// Eval анализа писем: гоняет analyzeEmail по evals/cases.json через живую Ollama.
// Обычный `go test` его не запускает (нужен тег eval), потому что он медленный и зависит от модели.
//
// Запуск из go-gateway:
//   go test -count=1 -tags eval -run TestAnalyzeEval -v .
//
// -count=1 обязательно: иначе Go может показать прошлый результат из кэша, а не прогнать модель заново.

package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

type evalCase struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Expect  struct {
		Suspicious           *bool    `json:"suspicious"`
		RequiresConfirmation *bool    `json:"requires_confirmation"`
		Urgency              []string `json:"urgency"`
	} `json:"expect"`
}

func TestAnalyzeEval(t *testing.T) {
	data, err := os.ReadFile("../evals/cases.json")
	if err != nil {
		t.Fatal(err)
	}

	var cases []evalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}

	model := getenvDefault("OLLAMA_MODEL", "granite4.2:3b")
	passed := 0

	for _, c := range cases {
		d, err := analyzeEmail("http://localhost:11434", model, c.From, c.Subject, c.Body)
		if err != nil {
			t.Errorf("[%s] ошибка: %v", c.ID, err)
			continue
		}

		var problems []string
		if e := c.Expect.Suspicious; e != nil && d.Suspicious != *e {
			problems = append(problems, "подозрительное="+boolRU(d.Suspicious))
		}
		if e := c.Expect.RequiresConfirmation; e != nil && d.RequiresConfirmation != *e {
			problems = append(problems, "подтверждение="+boolRU(d.RequiresConfirmation))
		}
		if len(c.Expect.Urgency) > 0 && !slices.Contains(c.Expect.Urgency, d.Urgency) {
			problems = append(problems, "срочность="+d.Urgency)
		}

		if len(problems) == 0 {
			passed++
			t.Logf("✅ [%s] %s | %s %s", c.ID, d.Urgency, d.SuggestedAction, d.SuspicionReason)
		} else {
			t.Logf("❌ [%s] %s | %s", c.ID, strings.Join(problems, ", "), d.SuggestedAction)
		}
	}

	t.Logf("ИТОГО: %d/%d", passed, len(cases))
}

func boolRU(b bool) string {
	if b {
		return "да"
	}
	return "нет"
}
