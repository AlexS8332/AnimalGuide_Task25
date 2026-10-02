package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/agent"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/agents"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/agents/agentstest"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/dialogs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/history"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm/llmtest"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/rag"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/runs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/server"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/store"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/tools"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/tools/toolstest"
)

// citeStub — подставной механизм rag.cite: итог хода (CiteView) по
// сценарию — источники из ожидаемых doc_id, «не знаю» на вопросе вне базы,
// память задачи на контрольной реплике. Проверяется клиент kb chat и
// проверки сценария на настоящем сервере и менеджере ходов, без базы и
// модели.
type citeStub struct {
	turns map[string]dialogs.Turn
	latin map[string]bool // реплики, где ответ должен быть с латынью и МСОП
}

func (h *citeStub) Name() string { return "rag" }

func (h *citeStub) Before(_ context.Context, t *runs.Turn) error {
	if !t.Features.On(features.RAGCite) {
		return nil
	}
	text := t.Request.Text
	tr, ok := h.turns[text]
	switch {
	case rag.Meta(text):
		t.Extra(string(features.RAGCite), rag.CiteView{Meta: true, MetaSource: rag.MetaSource})
	case ok && tr.Unknown:
		t.Extra(string(features.RAGCite), rag.CiteView{Cited: rag.Cited{Status: rag.StatusUnknown, Answer: "в базе знаний этого нет", Clarify: "о каком виде из базы спросить?"}})
	default:
		answer := "Ответ по базе."
		if h.latin[text] {
			answer = "Ответ по базе: Martes flavigula, статус LC."
		}
		v := rag.CiteView{Cited: rag.Cited{Status: rag.StatusAnswered, Answer: answer}, Check: rag.CiteCheck{OK: true, HasSources: true, HasQuotes: true}}
		docs := tr.Docs
		if !ok {
			docs = []string{"manul"}
		}
		for i, d := range docs {
			d, _, _ = strings.Cut(d, "|")
			id := d + "/structure/001"
			v.Sources = append(v.Sources, rag.CiteViewSrc{N: i + 1, ChunkID: id, Title: d, Path: "Раздел", Issued: true})
			v.Cited.Quotes = append(v.Cited.Quotes, rag.CitedQuote{ChunkID: id, Text: strings.Repeat("дословный кусок фрагмента ", 10)})
		}
		t.Extra(string(features.RAGCite), v)
	}
	return nil
}

func (h *citeStub) After(_ context.Context, t *runs.Turn) error {
	if v, ok := t.Extras[string(features.RAGCite)].(rag.CiteView); ok && v.Meta {
		v.Text = t.Result.Text
		t.Extra(string(features.RAGCite), v)
	}
	return nil
}

// chatApp — настоящий сервер приложения над менеджером ходов с подставной
// моделью: ведущий на контрольной реплике называет цель сценария.
func chatApp(t *testing.T, scenarios ...dialogs.Scenario) string {
	t.Helper()
	wiki, gbif := toolstest.NewWiki(), toolstest.NewGBIF()
	t.Cleanup(wiki.Close)
	t.Cleanup(gbif.Close)
	stub := &citeStub{turns: map[string]dialogs.Turn{}, latin: map[string]bool{}}
	goal := "Ответ ведущего."
	for _, s := range scenarios {
		goal = s.Goal.Text
		for i, tr := range s.Turns {
			stub.turns[tr.Text] = tr
			for _, r := range s.Rules {
				if r.Kind == dialogs.RuleLatin && r.Applies(i+1) {
					stub.latin[tr.Text] = true
				}
			}
		}
	}
	brain := &agentstest.Brain{LeadScript: func(req llm.Request, step int) llm.Response {
		if rag.Meta(agentstest.LastUser(req)) {
			return llmtest.Text("Наша цель: " + goal)
		}
		return llmtest.Text("Ответ ведущего.")
	}}
	reg := features.Catalog()
	m := runs.NewManager(runs.Config{
		Agents: agents.Deps{Runner: agent.Runner{LLM: &llmtest.Fake{Fn: brain.Chat}, Model: llm.DefaultModel}, Features: reg,
			Sources: agents.Local{Registry: tools.MustRegistry(tools.LocalTools(tools.NewFetcher(), wiki.URL, gbif.URL)...)}},
		Store:    history.NewStore(store.NewDir(t.TempDir())),
		Registry: reg, Defaults: reg.Defaults().With(features.Guard, false).With(features.Charter, false), Timeout: time.Minute,
		Hooks: []runs.Hook{stub},
	})
	srv := httptest.NewServer(server.New(m, fstest.MapFS{}, nil))
	t.Cleanup(srv.Close)
	return srv.URL
}

func scenarioFile(t *testing.T, name string) (string, dialogs.Scenario) {
	t.Helper()
	path := filepath.Join("..", "..", "eval", "dialogs", name)
	s, err := dialogs.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, s
}

// Оба сценария задания проходят на «идеальном» справочнике: клиент
// создаёт диалог с пресетом rag, ждёт ход по потоку событий, берёт итог
// rag.cite из истории, проверки сходятся.
func TestChatScript(t *testing.T) {
	for _, name := range []string{"a.json", "b.json"} {
		path, s := scenarioFile(t, name)
		url := chatApp(t, s)
		code, out, errOut := runKB("chat", "-url", url, "-script", path, "-corpus", filepath.Join("..", "..", "corpus"))
		if code != exitOK {
			t.Fatalf("%s: код %d\n%s\n%s", name, code, out, errOut)
		}
		if testing.Verbose() {
			t.Log(out)
		}
		for _, want := range []string{"Сценарий " + s.ID, "Источники:", "Цитаты:", "Проверки: [+] reply", "Источники: " + rag.MetaSource,
			"сценарий " + s.ID + " (" + itoa(len(s.Turns)) + " ходов) пройден", "Ход: маршрут lead"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s: в выводе нет %q\n%s", name, want, out)
			}
		}
		if s.ID == "B" && !strings.Contains(out, "Справочник: Не знаю — в базе знаний этого нет") || s.ID == "B" && !strings.Contains(out, "Уточните:") {
			t.Fatalf("B: нет «не знаю» и уточнения\n%s", out)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// Трасса -json: строка на ход и итог; провал проверки — код 1.
func TestChatScriptTraceAndFailure(t *testing.T) {
	path, s := scenarioFile(t, "a.json")
	// Без пресета rag (умолчания приложения): ответы мимо базы — проверки
	// источников проваливаются.
	url := chatApp(t, s)
	code, out, errOut := runKB("chat", "-url", url, "-script", path, "-preset", "", "-json")
	if code != exitFailed {
		t.Fatalf("код %d\n%s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(s.Turns)+1 {
		t.Fatalf("строк трассы %d", len(lines))
	}
	var first struct {
		N        int              `json:"n"`
		Observed dialogs.Observed `json:"observed"`
		Checks   []dialogs.Check  `json:"checks"`
		Turn     history.Turn     `json:"turn"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first.N != 1 || first.Turn.ID == "" || len(first.Turn.Events) == 0 {
		t.Fatalf("первая строка трассы: %v %+v", err, first)
	}
	var last struct {
		Passed  bool   `json:"passed"`
		Summary string `json:"summary"`
	}
	if json.Unmarshal([]byte(lines[len(lines)-1]), &last) != nil || last.Passed || !strings.Contains(last.Summary, "НЕ пройден") {
		t.Fatalf("итог трассы: %s", lines[len(lines)-1])
	}
	if !strings.Contains(errOut, "Источники: нет — ответ не из базы знаний") || !strings.Contains(errOut, "[-] sources") {
		t.Fatalf("человекочитаемый вывод в stderr:\n%s", errOut)
	}
}

func TestChatREPL(t *testing.T) {
	_, s := scenarioFile(t, "a.json")
	url := chatApp(t, s)
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader("Барханный кот\n/task\n/bogus\n\n/new\nНапомни, какая у нас цель и что мы решили.\n/quit\nне дойдёт\n")
	code, out, errOut := runKB("chat", "-url", url)
	if code != exitOK {
		t.Fatalf("код %d\n%s", code, errOut)
	}
	for _, want := range []string{"Диалог ", "Справочник: Ответ по базе.", "[1] sand-cat › Раздел (sand-cat/structure/001)",
		"Памяти задачи у диалога нет", "нет команды /bogus", "Новый диалог", "Наша цель: Доклад для школьников", "Источники: " + rag.MetaSource} {
		if !strings.Contains(out, want) {
			t.Fatalf("нет %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "не дойдёт") {
		t.Fatal("реплика после /quit")
	}
	// Приложение не запущено — понятная ошибка.
	code, _, errOut = runKB("chat", "-url", "http://127.0.0.1:1")
	if code != exitFailed || !strings.Contains(errOut, "приложение не отвечает") {
		t.Fatalf("без приложения: %d %s", code, errOut)
	}
	if code, _, errOut := runKB("chat", "-script", "нет-файла.json"); code != exitUsage {
		t.Fatalf("нет сценария: %d %s", code, errOut)
	}
}
