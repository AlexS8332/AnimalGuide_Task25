package bench

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/agents/agentstest"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/dialogs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/embed"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/kb"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm/llmtest"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/rag"
)

var chatScenarioFiles = []string{"../../eval/dialogs/a.json", "../../eval/dialogs/b.json"}

// chatModel — подставная справочная для И-13 поверх модели стенда:
// извлекатель ставит цель на реплике goal_set; ведущий отвечает kb_answer
// с дословной цитатой из выдачи хода (латынь и статус МСОП — там, где их
// требует сценарий), «не знаю» — на вопросе вне базы, а на контрольной
// реплике называет цель из блока памяти задачи (блока нет — не помнит).
// broken — реплика, на которой ведущий отвечает без источников.
type chatModel struct {
	goals  map[string]string       // реплика goal_set → цель сценария
	turns  map[string]dialogs.Turn // реплика → её ожидания
	latin  map[string]bool         // реплики, где ответ с латынью и МСОП
	broken string
	// unknown — вопрос по базе, на который ведущий отвечает «не знаю».
	unknown string
}

func newChatModel(t *testing.T) *chatModel {
	t.Helper()
	m := &chatModel{goals: map[string]string{}, turns: map[string]dialogs.Turn{}, latin: map[string]bool{}}
	for _, p := range chatScenarioFiles {
		s, err := dialogs.Load(p)
		if err != nil {
			t.Fatal(err)
		}
		for i, tr := range s.Turns {
			m.turns[tr.Text] = tr
			if tr.Has(dialogs.MarkGoalSet) {
				m.goals[tr.Text] = s.Goal.Text
			}
			for _, r := range s.Rules {
				if (r.Kind == dialogs.RuleLatin || r.Kind == dialogs.RuleIUCN) && r.Applies(i+1) {
					m.latin[tr.Text] = true
				}
			}
		}
	}
	return m
}

func (m *chatModel) extract(req llm.Request) (string, error) {
	user := agentstest.LastUser(req)
	_, text, _ := strings.Cut(user, "=== Новая реплика пользователя ===\n")
	text = strings.TrimSpace(text)
	if goal, ok := m.goals[text]; ok {
		var out struct {
			Profile struct {
				Set []any `json:"set"`
			} `json:"profile"`
			Task struct {
				Goal struct {
					Text  string `json:"text"`
					Quote string `json:"quote"`
				} `json:"goal"`
			} `json:"task"`
		}
		out.Profile.Set = []any{}
		out.Task.Goal.Text, out.Task.Goal.Quote = goal, text
		return agentstest.Args(out), nil
	}
	return `{"profile":{"set":[]},"memory":{"set":[]},"facts":{"set":[]}}`, nil
}

// taskGoal — цель из блока памяти задачи в запросе ведущего.
func taskGoal(req llm.Request) string {
	for _, msg := range req.Messages {
		if msg.Role != llm.RoleSystem || !strings.Contains(msg.Content, "Задача разговора") {
			continue
		}
		for _, line := range strings.Split(msg.Content, "\n") {
			if g, ok := strings.CutPrefix(strings.TrimSpace(line), "Цель: "); ok {
				return g
			}
		}
	}
	return ""
}

func (m *chatModel) lead(req llm.Request, _ int) llm.Response {
	user := agentstest.LastUser(req)
	if rag.Meta(user) {
		if g := taskGoal(req); g != "" {
			return llmtest.Text("Наша цель: " + g)
		}
		return llmtest.Text("Не помню, о чём мы договаривались вначале.")
	}
	if !llmtest.HasTool(req, rag.FinishName) {
		return llmtest.Text("Ответ ведущего.")
	}
	var res rag.SearchResult
	_ = json.Unmarshal([]byte(agentstest.LastReply(req, rag.ToolName)), &res)
	tr := m.turns[user]
	if tr.Unknown || len(res.Hits) == 0 || user == m.unknown {
		return llmtest.ToolCall(rag.FinishName, agentstest.Args(rag.Cited{Status: rag.StatusUnknown,
			Answer: "в базе знаний этого нет", Clarify: "Рассказать о другом виде из базы?"}))
	}
	h := res.Hits[0]
	answer := "Ответ по базе знаний."
	if m.latin[user] {
		answer = "Харза (Martes flavigula), статус МСОП LC."
	}
	c := rag.Cited{Status: rag.StatusAnswered, Answer: answer,
		Sources: []rag.CitedSource{{ChunkID: h.ChunkID}},
		Quotes:  []rag.CitedQuote{{ChunkID: h.ChunkID, Text: string([]rune(h.Text)[:min(60, len([]rune(h.Text)))])}}}
	if user == m.broken {
		c.Sources, c.Quotes = nil, nil
	}
	return llmtest.ToolCall(rag.FinishName, agentstest.Args(c))
}

// chatKB — база ragKB с низким порогом фильтра: hash-эмбеддер даёт косинусы
// ниже порога по умолчанию, и фильтр отсёк бы всю выдачу.
func chatKB(t *testing.T) string {
	t.Helper()
	path := ragKB(t)
	ctx := context.Background()
	st, err := kb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMinScore(ctx, rag.DefaultIndex, 0.01); err != nil {
		t.Fatal(err)
	}
	return path
}

func chatRig(t *testing.T) (*rig, *chatModel, *Chat) {
	t.Helper()
	r := newRig(t)
	m := newChatModel(t)
	r.brain.Extract = m.extract
	r.brain.LeadScript = m.lead
	r.env.LLM = r.fake
	return r, m, &Chat{KBPath: chatKB(t), Scenarios: chatScenarioFiles, Embedder: embed.Hash{}}
}

func TestChatTrial(t *testing.T) {
	r, _, tr := chatRig(t)
	res := r.run(t, tr)
	if res.Mechanism != features.Task || len(res.Lanes) != 3 || res.Lanes[1].Diff != "−task" || res.Lanes[2].Diff != "те же механизмы" {
		t.Fatalf("дорожки: %s %+v", res.Mechanism, res.Lanes)
	}
	for _, f := range []string{"rag", "rag.filter", "rag.rewrite", "rag.cite", "task"} {
		if !strings.Contains(strings.Join(res.Lanes[0].Features, ","), f) {
			t.Fatalf("основная без %s: %v", f, res.Lanes[0].Features)
		}
	}
	if res.Verdict() != Pass {
		t.Fatalf("вердикт %s: %+v\n%v", res.Verdict(), res.Checks, res.Notes)
	}
	if c := find(t, res, "источники показаны в каждом ответе (по базе, «не знаю», память задачи)", laneChatTask); c.Got != "29 из 29" {
		t.Fatalf("источники: %+v", c)
	}
	if c := find(t, res, "цель названа на контрольных репликах", ""); c.Got != "A 3/3, B 3/3" {
		t.Fatalf("цель: %+v", c)
	}
	// Окно 12: ход с базой кладёт в историю 6 сообщений (реплика, вызов и
	// выдача kb_search, вызов и приём kb_answer, ответ), контрольный — 2;
	// цель выпадает на 4-м ходу.
	kept := find(t, res, "цель удержана в памяти задачи после выпадения из окна", "")
	if kept.Status != Pass || !strings.HasSuffix(kept.Got, "из 23") || !strings.HasPrefix(kept.Got, "23 ") {
		t.Fatalf("цель в памяти: %+v", kept)
	}
	if c := find(t, res, "ограничения ответа не нарушены", ""); !strings.HasPrefix(c.Got, "0 из ") {
		t.Fatalf("ограничения: %+v", c)
	}
	// Ведущий — один запрос на ход; извлекатель — не на каждой реплике (на
	// вопросах о животном и на «напомни цель» — нет).
	if c := find(t, res, "запросов к модели на ход (среднее и максимум)", ""); c.Status != Pass || !strings.HasPrefix(c.Got, "1.24 (36 на 29 ходов), максимум 2 (A·1)") {
		t.Fatalf("запросы: %+v", c)
	}
	if c := find(t, res, "доля кэша на дорожке", ""); !strings.HasPrefix(c.Got, "70 %") {
		t.Fatalf("кэш: %+v", c)
	}
	// B-8 с маркой restart: стенд перезапущен, задача ветки та же, и
	// дорожки после перезапуска не перепутались (без памяти задачи цель
	// по-прежнему не названа — ниже).
	if c := find(t, res, "задача ветки та же после перезапуска приложения", ""); c.Status != Pass || c.Got != "1 из 1" {
		t.Fatalf("перезапуск: %+v", c)
	}
	if v := metric(res, "ответ по базе там, где ждали (проверка sources, отчётно)", laneChatTask); v != "29 из 29" {
		t.Fatalf("sources: %q", v)
	}
	if v := metric(res, "опора на цитаты: answered с grounded (отчётно)", laneChatTask); !strings.Contains(v, " из 22") {
		t.Fatalf("опора: %q", v)
	}
	if v := metric(res, "обязательные числа и слова в ответе (must, отчётно)", laneChatTask); !strings.HasPrefix(v, "0 из 2 (нет: ") {
		t.Fatalf("must: %q", v)
	}
	// Без памяти задачи цель на контрольных не названа: разница больше
	// шума (повтор совпал с основной).
	if v := metric(res, "цель на контрольных репликах", laneChatNoTask); !strings.HasPrefix(v, "0 из 6") {
		t.Fatalf("без задачи: %q", v)
	}
	if v := metric(res, "цель в памяти задачи после выпадения из окна", laneChatNoTask); !strings.Contains(v, "механизм выключен") {
		t.Fatalf("без задачи, память: %q", v)
	}
	if v := metric(res, "разница: цель названа на контрольных", laneChatDiff); v != "+6 (шум ±0 — |основная − повтор|): больше шума" {
		t.Fatalf("разница: %q", v)
	}
	if v := metric(res, "ожидаемые doc_id в источниках (отчётно)", laneChatTask); !strings.Contains(v, " из 22") {
		t.Fatalf("doc_id: %q", v)
	}
	if len(res.Stats) != 3 || res.Stats[0].Turns != 29 || res.Stats[0].Brak != 0 {
		t.Fatalf("цена дорожек: %+v", res.Stats)
	}
	if len(res.Samples) != 4 || res.Samples[0].Lane != laneChatTask || res.Samples[1].Lane != laneChatNoTask ||
		!strings.Contains(res.Samples[0].Reply, "Доклад для школьников") || !strings.Contains(res.Samples[2].Topic, "вне базы") ||
		!strings.Contains(res.Samples[3].Topic, "смена ограничения") {
		t.Fatalf("ответы: %+v", res.Samples)
	}
}

// Ответ без источников — провал; цель без памяти задачи — только
// отчётная разница, не провал основной.
func TestChatTrialBrokenSource(t *testing.T) {
	r, m, tr := chatRig(t)
	m.broken = "Сравни харзу и соболя по массе."
	res := r.run(t, tr)
	c := find(t, res, "источники показаны в каждом ответе (по базе, «не знаю», память задачи)", "")
	if c.Status != Fail || c.Got != "28 из 29" || !strings.Contains(c.Note, "B·6") {
		t.Fatalf("источники: %+v", c)
	}
	mustPass(t, res, "цель названа на контрольных репликах", "цель удержана в памяти задачи после выпадения из окна")
	if res.Verdict() != Fail {
		t.Fatalf("вердикт %s", res.Verdict())
	}
}

// «Не знаю» на вопросе по базе: источники показаны (ближайшее или пустая
// выдача — по ТЗ это показ), жёсткая проверка пройдена; что ответа по
// базе не было там, где ждали, — отчётная строка.
func TestChatTrialUnknownWhereExpected(t *testing.T) {
	r, m, tr := chatRig(t)
	m.unknown = "Где в Азии живёт камышовый кот?"
	res := r.run(t, tr)
	mustPass(t, res, "источники показаны в каждом ответе (по базе, «не знаю», память задачи)")
	if v := metric(res, "ответ по базе там, где ждали (проверка sources, отчётно)", laneChatTask); !strings.HasPrefix(v, "28 из 29 (нет: A·12 (unknown") {
		t.Fatalf("sources: %q", v)
	}
}

// Нет базы или эмбеддера — «не определено» с причиной; без модели — ошибка
// стенда.
func TestChatTrialPending(t *testing.T) {
	r, _, tr := chatRig(t)
	tr.KBPath = filepath.Join(t.TempDir(), "nope.db")
	res := r.run(t, tr)
	if res.Count(Pending) != len(chatChecks) || res.Count(Fail) != 0 || !strings.Contains(res.Checks[0].Note, "go run ./cmd/kb index") ||
		len(res.Lanes) != 3 {
		t.Fatalf("без базы: %+v", res.Checks)
	}
	tr.KBPath, tr.Embedder = chatKB(t), nil
	t.Setenv(embed.EnvBaseURL, "http://127.0.0.1:1")
	if res := r.run(t, tr); res.Count(Pending) != len(chatChecks) || !strings.Contains(res.Checks[0].Note, "эмбеддер недоступен") {
		t.Fatalf("без эмбеддера: %+v", res.Checks)
	}
	r.env.LLM = nil
	if res := RunOne(context.Background(), r.env, tr); !strings.Contains(res.Err, "Env.LLM") {
		t.Fatalf("без модели: %q", res.Err)
	}
}
