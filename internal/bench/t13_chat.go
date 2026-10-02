package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/agents"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/dialogs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/embed"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/history"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/llm"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
)

// Дорожки И-13: справочная по пресету rag с памятью задачи, без неё и
// повтор основной (шум модели).
const (
	laneChatTask   = "основная"
	laneChatNoTask = "без памяти задачи"
	laneChatRepeat = "повтор"
	// laneChatDiff — строка отчёта «что даёт память задачи».
	laneChatDiff = "основная − без памяти задачи"
)

// Пороги И-13 (предложение, задание 25).
const (
	chatGoalKept = 0.9 // цель в памяти задачи после выпадения из окна
	chatMaxCalls = 4.0 // запросов к модели на ход в среднем (ТЗ, раздел 10)
	chatPeakCall = 12  // и не больше стольких на любом ходе
	chatMinCache = 0.6 // доля токенов запроса из кэша
)

// DefaultChatScenarios — длинные сценарии задания 25 (eval/dialogs).
var DefaultChatScenarios = []string{"eval/dialogs/a.json", "eval/dialogs/b.json"}

// Chat — И-13: «Мини-чат: цель и источники в длинном диалоге».
//
// Приложение со справочной (пресет rag: rag, rag.filter, rag.rewrite,
// rag.cite, task поверх умолчаний) гоняет два длинных сценария
// (eval/dialogs, 14–15 реплик) на трёх дорожках в ногу: основная, без
// памяти задачи (−task, CheckLanes) и повтор основной (Same — шум
// модели). Каждый сценарий — свой стенд и свои диалоги. Ход проверяется
// так же, как kb chat -script: dialogs.CheckTurn по наблюдению из записи
// хода (dialogs.FromTurn: ответ и итог rag.cite).
//
// Жёсткие проверки — на основной дорожке, пороги ТЗ:
//
//  1. источники показаны в каждом ответе (ТЗ, раздел 10;
//     dialogs.SourcesShown): ответ по базе — со списком источников, «не
//     знаю» — с ближайшим найденным или с пустой выдачей, ответ о разговоре
//     — источник «память задачи»; ход с ошибкой — провал;
//  2. цель названа на всех контрольных репликах каждого сценария (3 из 3);
//  3. цель удержана в памяти задачи: на ходах, когда реплика с целью уже
//     выпала из окна, цель ветки непуста, а в задаче ветки
//     (task.State.Render — цель и уточнения: «школьники», «Азия» могут
//     жить в уточнениях) есть все ключевые слова цели сценария — ≥ 90 %
//     таких ходов;
//  4. ограничения сценария (no_latin, latin, max_sentences, iucn) — 0
//     нарушений («не определить» не считается);
//  5. запросов к модели на ход — ≤ 4 в среднем и ≤ 12 на любом ходе;
//  6. доля кэша — ≥ 60 % токенов запроса;
//  7. перезапуск: перед репликой с маркой restart стенд перезапускает
//     приложение (Stand.Restart), и задача ветки после него та же, что до.
//
// «Выпала из окна» считается по сообщениям истории: перед ходом в истории
// H сообщений, реплика с целью — сообщение с номером g (с нуля); окно
// уходит модели последними Window сообщениями (history.Window), и реплики
// с целью в нём нет, когда H − Window > g.
//
// Отчётно — «ответ по базе там, где ждали» (проверка sources: по базе на
// вопросе по базе, «не знаю» вне базы), ожидаемые doc_id в источниках,
// опора ответов на цитаты (доля answered с grounded), must, те же числа на
// двух других дорожках и разница «основная − без памяти задачи» против
// шума (|основная − повтор|): засчитывается только разница больше шума.
//
// Нет kb.db или эмбеддера — проверки «не определено» с причиной. Без
// модели — ошибка стенда, как у И-10.
type Chat struct {
	// KBPath — kb.db; пусто — Env.KB, иначе kb.db в корне репозитория.
	KBPath string
	// Scenarios — пусто → DefaultChatScenarios.
	Scenarios []string
	// Embedder — nil → embed.FromEnv, если сайдкар отвечает.
	Embedder embed.Embedder
	// Window — окно диалога с rag.cite в сообщениях; 0 → history.CiteWindow
	// (как у приложения без флага -window).
	Window int
}

// NewChat — И-13 с настройками по умолчанию.
func NewChat() *Chat { return &Chat{} }

func (t *Chat) ID() string { return "И-13" }
func (t *Chat) Title() string {
	return "Мини-чат: цель и источники в длинном диалоге"
}

// chatChecks — жёсткие проверки основной дорожки.
var chatChecks = []string{
	"источники показаны в каждом ответе (по базе, «не знаю», память задачи)",
	"цель названа на контрольных репликах",
	"цель удержана в памяти задачи после выпадения из окна",
	"ограничения ответа не нарушены",
	"запросов к модели на ход (среднее и максимум)",
	"доля кэша на дорожке",
	"задача ветки та же после перезапуска приложения",
}

// chatLanes — дорожки И-13 поверх умолчаний приложения.
func chatLanes(reg *features.Registry, base features.Set) ([]Lane, error) {
	on, err := reg.ParsePreset(features.PresetRAG, "", base)
	if err != nil {
		return nil, err
	}
	return []Lane{
		{Name: laneChatTask, Note: "справочная по пресету rag: база, фильтр, переписывание, ответ с источниками и память задачи", Features: on},
		{Name: laneChatNoTask, Note: "то же без памяти задачи: цель живёт только в окне истории", Features: on.With(features.Task, false)},
		{Name: laneChatRepeat, Note: "повтор основной: шум модели", Features: on, Same: true},
	}, nil
}

// chatPlay — сценарий на одной дорожке: отчёт dialogs и наблюдения окна.
type chatPlay struct {
	rep dialogs.Report
	// before — сообщений истории перед следующим ходом; goalAt — номер
	// сообщения реплики с целью (-1 — ещё не было).
	before, goalAt int
	// out — ходы после выпадения цели из окна; kept — из них цель в
	// памяти задачи; miss — где нет.
	out, kept int
	miss      []string
	// restarts — перезапусков перед ходами сценария; same — из них задача
	// ветки после перезапуска та же; changed — где нет.
	restarts, same int
	changed        []string
}

// chatLane — итог дорожки по обоим сценариям.
type chatLane struct {
	plays []*chatPlay
	turns int
	calls int
	// peak — больше всего запросов к модели на одном ходе; peakAt — где.
	peak   int
	peakAt string
	usage  llm.Usage
	cost  llm.Cost
}

func (t *Chat) Run(ctx context.Context, s *Stand, r *Result) error {
	r.Goal = "в длинном разговоре со справочной ассистент не теряет цель и на каждый вопрос отвечает с источниками, соблюдая договорённости"
	r.Mechanism = features.Task
	if s.env.LLM == nil {
		return errors.New("И-13: стенду не передан клиент модели (Env.LLM)")
	}
	lanes, err := chatLanes(s.env.Registry, s.env.Base)
	if err != nil {
		return fmt.Errorf("И-13: пресет %s: %w", features.PresetRAG, err)
	}
	if _, err := CheckLanes(s.env.Registry, lanes); err != nil {
		return fmt.Errorf("И-13: %w", err)
	}
	r.describeLanes(s.env.Registry, lanes)

	var scs []dialogs.Scenario
	for _, p := range t.scenarios() {
		sc, err := dialogs.Load(p)
		if err == nil {
			err = dialogs.Validate(sc, nil)
		}
		if err != nil {
			r.yes("сценарии читаются", laneChatTask, false, p+": "+err.Error())
			return nil
		}
		scs = append(scs, sc)
	}
	pending := func(why string) {
		for _, c := range chatChecks {
			r.pending(c, "—", laneChatTask, why)
		}
	}
	path := t.kbPath(s)
	if _, err := os.Stat(path); err != nil {
		pending(fmt.Sprintf("базы знаний нет: %s — соберите: go run ./cmd/kb index -strategy all", path))
		return nil
	}
	if t.Embedder == nil {
		if hs := embed.FromEnv().Health(ctx); !hs.OK {
			pending("эмбеддер недоступен (" + hs.Why + "): фильтр и «не знаю» кодом — по косинусу dense, по BM25 справочная не та, что у человека; поднимите сайдкар: uv run embedder/server.py")
			return nil
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	sub, err := s.Sub("chat", Options{KB: abs})
	if err != nil {
		return err
	}

	window := t.Window
	if window <= 0 {
		window = history.CiteWindow
	}
	res := map[string]*chatLane{}
	for _, l := range lanes {
		res[l.Name] = &chatLane{}
	}
	for _, sc := range scs {
		if err := t.play(ctx, sub, lanes, sc, window, res); err != nil {
			return err
		}
	}
	t.judge(r, scs, res)
	t.report(r, lanes, scs, res)
	t.samples(r, scs, res)
	if dir := sub.Dir(); dir != "" {
		out := map[string][]dialogs.Report{}
		for name, x := range res {
			for _, p := range x.plays {
				out[name] = append(out[name], p.rep)
			}
		}
		if data, err := json.MarshalIndent(out, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "dialogs.json"), data, 0o644)
		}
	}
	return nil
}

func (t *Chat) kbPath(s *Stand) string {
	if t.KBPath != "" {
		return t.KBPath
	}
	if s.env.KB != "" {
		return s.env.KB
	}
	return DefaultCacheDB
}

func (t *Chat) scenarios() []string {
	if len(t.Scenarios) == 0 {
		return DefaultChatScenarios
	}
	return t.Scenarios
}

// play — сценарий на всех дорожках в ногу, в новых диалогах.
func (t *Chat) play(ctx context.Context, sub *Stand, lanes []Lane, sc dialogs.Scenario, window int, res map[string]*chatLane) error {
	g, err := sub.Group("И-13: сценарий "+sc.ID+" — "+sc.Title, lanes)
	if err != nil {
		return err
	}
	plays := map[string]*chatPlay{}
	windowed := map[string]bool{}
	for _, l := range lanes {
		p := &chatPlay{rep: dialogs.Report{Scenario: sc.ID, Title: sc.Title}, goalAt: -1}
		plays[l.Name] = p
		res[l.Name].plays = append(res[l.Name].plays, p)
		windowed[l.Name] = sub.env.Registry.Complete(l.Features).On(features.Window)
	}
	sub.env.logf("  И-13: сценарий %s (%d реплик) — %s", sc.ID, len(sc.Turns), sc.Title)
	for i, turn := range sc.Turns {
		n := i + 1
		if turn.Has(dialogs.MarkRestart) {
			if err := t.restart(sub, g, sc, n, plays); err != nil {
				return err
			}
		}
		steps, err := g.Send(ctx, agents.Request{Text: turn.Text})
		if err != nil {
			return fmt.Errorf("И-13: сценарий %s, реплика %d: %w", sc.ID, n, err)
		}
		for _, st := range steps {
			p, x := plays[st.Lane], res[st.Lane]
			if p.goalAt < 0 && (turn.Has(dialogs.MarkGoalSet) || n == 1) {
				p.goalAt = p.before
			}
			o := dialogs.FromTurn(st.Turn)
			tr := dialogs.TurnReport{N: n, Text: turn.Text, Marks: turn.Marks, Observed: o, Checks: dialogs.CheckTurn(sc, n, o)}
			p.rep.Turns = append(p.rep.Turns, tr)

			// Реплика с целью вне окна этого хода — что в памяти задачи
			// ветки после хода.
			if windowed[st.Lane] && p.goalAt >= 0 && p.before-window > p.goalAt {
				p.out++
				state := st.Detail.Task.Render()
				if strings.TrimSpace(st.Detail.Task.Goal) != "" && len(dialogs.GoalNamed(state, sc.Goal.Keywords)) == 0 {
					p.kept++
				} else {
					p.miss = append(p.miss, fmt.Sprintf("%s·%d «%s»", sc.ID, n, clip(oneLine(state), 60)))
				}
			}
			if st.Detail.ID != "" {
				p.before = st.Detail.Messages
			}
			x.turns++
			x.calls += st.Turn.Totals.LLMCalls
			if st.Turn.Totals.LLMCalls > x.peak {
				x.peak, x.peakAt = st.Turn.Totals.LLMCalls, fmt.Sprintf("%s·%d", sc.ID, n)
			}
			x.usage = x.usage.Add(st.Turn.Totals.Usage)
			x.cost = x.cost.Add(st.Turn.Totals.Cost)
			if bad := tr.Failed(); len(bad) > 0 {
				sub.env.logf("  И-13 %s·%d %s: не пройдено — %s", sc.ID, n, st.Lane, checkNames(bad))
			}
		}
	}
	for _, l := range lanes {
		sub.env.logf("  И-13 %s: %s", l.Name, plays[l.Name].rep.Summary())
	}
	return nil
}

// restart — перезапуск приложения перед ходом n (марка restart): снимок
// задачи ветки каждой дорожки до, Stand.Restart, снимок после — задача та
// же (файл диалога пережил перезапуск вместе с задачей ветки).
func (t *Chat) restart(sub *Stand, g *Group, sc dialogs.Scenario, n int, plays map[string]*chatPlay) error {
	before := map[string]task.State{}
	for _, d := range g.Dialogs {
		if det, ok := d.Detail(); ok {
			before[d.Lane.Name] = det.Task
		}
	}
	sub.env.logf("  И-13: сценарий %s, перезапуск приложения перед репликой %d", sc.ID, n)
	if err := sub.Restart(); err != nil {
		return fmt.Errorf("И-13: сценарий %s, перезапуск перед репликой %d: %w", sc.ID, n, err)
	}
	for _, d := range g.Dialogs {
		p := plays[d.Lane.Name]
		p.restarts++
		det, ok := d.Detail()
		was := before[d.Lane.Name]
		switch {
		case !ok:
			p.changed = append(p.changed, fmt.Sprintf("%s·%d: диалог не поднялся", sc.ID, n))
		case det.Task.Render() != was.Render() || det.Task.Version != was.Version:
			p.changed = append(p.changed, fmt.Sprintf("%s·%d: v%d «%s» → v%d «%s»", sc.ID, n,
				was.Version, clip(oneLine(was.Render()), 50), det.Task.Version, clip(oneLine(det.Task.Render()), 50)))
		default:
			p.same++
		}
	}
	return nil
}

// oneLine — строки задачи через «; » (или прочерк).
func oneLine(s string) string { return orDash(strings.ReplaceAll(s, "\n", "; ")) }

func checkNames(cs []dialogs.Check) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name+" ("+c.Got+")")
	}
	return strings.Join(out, ", ")
}

// chatTally — счёт дорожки по проверкам ходов.
type chatTally struct {
	shown, shownTotal     int
	shownMiss             []string
	sources, sourcesTotal int
	srcMiss               []string
	grounded, answered    int
	groundMiss            []string
	must, mustTotal       int
	mustMiss              []string
	restarts, same        int
	changed               []string
	docs, docsTotal       int
	violations, ruled     int
	violMiss              []string
	goal, goalTotal       int
	goalBy                []string // «A 3/3» по сценариям
	goalFull              bool     // во всех сценариях названа на всех контрольных
	goalMiss              []string
	kept, out             int
	keptMiss              []string
}

func isRule(name string) bool {
	switch name {
	case dialogs.RuleNoLatin, dialogs.RuleLatin, dialogs.RuleMaxSentences, dialogs.RuleIUCN:
		return true
	}
	return false
}

func tallyOf(x *chatLane) chatTally {
	c := chatTally{goalFull: true}
	for _, p := range x.plays {
		ok, total := 0, 0
		for _, tr := range p.rep.Turns {
			id := fmt.Sprintf("%s·%d", p.rep.Scenario, tr.N)
			c.shownTotal++
			if ok, got := dialogs.SourcesShown(tr.Observed); ok {
				c.shown++
			} else {
				c.shownMiss = append(c.shownMiss, id+" ("+got+")")
			}
			c.sourcesTotal++
			src := false
			for _, ch := range tr.Checks {
				switch {
				case ch.Name == dialogs.CheckSources:
					src = ch.OK
				case ch.Name == dialogs.CheckGrounded && !ch.NA:
					c.answered++
					if ch.OK {
						c.grounded++
					} else {
						c.groundMiss = append(c.groundMiss, id+": "+ch.Got)
					}
				case ch.Name == dialogs.CheckMust:
					c.mustTotal++
					if ch.OK {
						c.must++
					} else {
						c.mustMiss = append(c.mustMiss, id+" "+ch.Got)
					}
				case ch.Name == dialogs.CheckDocs:
					c.docsTotal++
					if ch.OK {
						c.docs++
					}
				case ch.Name == dialogs.CheckGoal:
					total++
					if ch.OK {
						ok++
					} else {
						c.goalMiss = append(c.goalMiss, id+" «"+clip(tr.Observed.Reply, 80)+"»")
					}
				case isRule(ch.Name) && !ch.NA:
					c.ruled++
					if !ch.OK {
						c.violations++
						c.violMiss = append(c.violMiss, id+" "+ch.Name+": "+ch.Got)
					}
				}
			}
			if src {
				c.sources++
			} else {
				c.srcMiss = append(c.srcMiss, id+" ("+tr.Observed.Status()+")")
			}
		}
		c.goal += ok
		c.goalTotal += total
		c.goalBy = append(c.goalBy, fmt.Sprintf("%s %d/%d", p.rep.Scenario, ok, total))
		if total == 0 || ok < total {
			c.goalFull = false
		}
		c.kept += p.kept
		c.out += p.out
		c.keptMiss = append(c.keptMiss, p.miss...)
		c.restarts += p.restarts
		c.same += p.same
		c.changed = append(c.changed, p.changed...)
	}
	return c
}

// judge — жёсткие проверки основной дорожки.
func (t *Chat) judge(r *Result, scs []dialogs.Scenario, res map[string]*chatLane) {
	x := res[laneChatTask]
	c := tallyOf(x)

	ch := Check{What: chatChecks[0], Want: "100 % ходов", Lane: laneChatTask, Got: fmt.Sprintf("%d из %d", c.shown, c.shownTotal), Status: Pass}
	switch {
	case c.shownTotal == 0:
		ch.Status, ch.Note = Fail, "ходов нет"
	case c.shown < c.shownTotal:
		ch.Status, ch.Note = Fail, "не показаны: "+strings.Join(firstN(c.shownMiss, 6), ", ")
	}
	r.check(ch)

	ch = Check{What: chatChecks[1], Want: "все контрольные в каждом сценарии (ТЗ: 3 из 3)", Lane: laneChatTask,
		Got: strings.Join(c.goalBy, ", "), Status: Pass}
	if !c.goalFull {
		ch.Status = Fail
		ch.Note = strings.Join(firstN(c.goalMiss, 3), "; ")
	}
	r.check(ch)

	ch = Check{What: chatChecks[2], Want: fmt.Sprintf("≥ %.0f %% ходов", 100*chatGoalKept), Lane: laneChatTask,
		Got: fmt.Sprintf("%d из %d", c.kept, c.out), Status: Pass}
	switch {
	case c.out == 0:
		ch.Status, ch.Got, ch.Note = Pending, "—", "реплика с целью не выпала из окна ни в одном сценарии (или окно выключено)"
	case float64(c.kept) < chatGoalKept*float64(c.out):
		ch.Status = Fail
	}
	if len(c.keptMiss) > 0 {
		ch.Note = joinText(ch.Note, "нет цели: "+strings.Join(firstN(c.keptMiss, 4), ", "))
	}
	r.check(ch)

	ch = Check{What: chatChecks[3], Want: "0", Lane: laneChatTask, Got: fmt.Sprintf("%d из %d определимых", c.violations, c.ruled), Status: Pass}
	if c.violations > 0 {
		ch.Status, ch.Note = Fail, strings.Join(firstN(c.violMiss, 4), "; ")
	}
	r.check(ch)

	per := 0.0
	if x.turns > 0 {
		per = float64(x.calls) / float64(x.turns)
	}
	ch = Check{What: chatChecks[4], Want: fmt.Sprintf("среднее ≤ %.0f и максимум ≤ %d", chatMaxCalls, chatPeakCall), Lane: laneChatTask,
		Got: fmt.Sprintf("%.2f (%d на %d ходов), максимум %d (%s)", per, x.calls, x.turns, x.peak, orDash(x.peakAt)), Status: Pass}
	if x.turns == 0 || per > chatMaxCalls || x.peak > chatPeakCall {
		ch.Status = Fail
	}
	r.check(ch)

	ch = Check{What: chatChecks[5], Want: fmt.Sprintf("≥ %.0f %%", 100*chatMinCache), Lane: laneChatTask, Status: Pass}
	if x.usage.Prompt == 0 {
		ch.Status, ch.Got, ch.Note = Pending, "—", "usage без токенов запроса: кэш не измерить"
	} else {
		share := float64(x.usage.CacheHit) / float64(x.usage.Prompt)
		ch.Got = fmt.Sprintf("%.0f %% (%d из %d)", 100*share, x.usage.CacheHit, x.usage.Prompt)
		if share < chatMinCache {
			ch.Status = Fail
		}
	}
	r.check(ch)

	ch = Check{What: chatChecks[6], Want: "та же на каждом перезапуске", Lane: laneChatTask,
		Got: fmt.Sprintf("%d из %d", c.same, c.restarts), Status: Pass}
	switch {
	case c.restarts == 0:
		ch.Status, ch.Got, ch.Note = Pending, "—", "в сценариях нет реплики с маркой restart"
	case c.same < c.restarts:
		ch.Status, ch.Note = Fail, strings.Join(firstN(c.changed, 3), "; ")
	}
	r.check(ch)
}

// missNote — « (нет: …)» по первым трём провалам или пусто.
func missNote(miss []string) string {
	if len(miss) == 0 {
		return ""
	}
	return " (нет: " + strings.Join(firstN(miss, 3), "; ") + ")"
}

// report — отчётные числа всех дорожек и разница «с памятью задачи — без»
// против шума.
func (t *Chat) report(r *Result, lanes []Lane, scs []dialogs.Scenario, res map[string]*chatLane) {
	tallies := map[string]chatTally{}
	for _, l := range lanes {
		x := res[l.Name]
		c := tallyOf(x)
		tallies[l.Name] = c
		r.metric("источники показаны", l.Name, "%d из %d", c.shown, c.shownTotal)
		r.metric("ответ по базе там, где ждали (проверка sources, отчётно)", l.Name, "%d из %d%s", c.sources, c.sourcesTotal, missNote(c.srcMiss))
		r.metric("опора на цитаты: answered с grounded (отчётно)", l.Name, "%d из %d%s", c.grounded, c.answered, missNote(c.groundMiss))
		if c.mustTotal > 0 {
			r.metric("обязательные числа и слова в ответе (must, отчётно)", l.Name, "%d из %d%s", c.must, c.mustTotal, missNote(c.mustMiss))
		}
		r.metric("цель на контрольных репликах", l.Name, "%d из %d (%s)", c.goal, c.goalTotal, strings.Join(c.goalBy, ", "))
		if l.Features.On(features.Task) {
			r.metric("цель в памяти задачи после выпадения из окна", l.Name, "%d из %d", c.kept, c.out)
		} else {
			r.metric("цель в памяти задачи после выпадения из окна", l.Name, "%d из %d (механизм выключен — задача не пишется)", c.kept, c.out)
		}
		r.metric("нарушений ограничений", l.Name, "%d из %d определимых", c.violations, c.ruled)
		r.metric("ожидаемые doc_id в источниках (отчётно)", l.Name, "%d из %d", c.docs, c.docsTotal)
		if x.turns > 0 {
			r.metric("запросов к модели на ход", l.Name, "%.2f, максимум %d", float64(x.calls)/float64(x.turns), x.peak)
		}
		if x.usage.Prompt > 0 {
			r.metric("доля кэша", l.Name, "%.0f %%", 100*float64(x.usage.CacheHit)/float64(x.usage.Prompt))
		}
		r.metric("цена дорожки (оба сценария)", l.Name, "$%.4f", x.cost.USD)
		for _, p := range x.plays {
			r.note("%s: %s", l.Name, p.rep.Summary())
		}
	}
	m, no, rep := tallies[laneChatTask], tallies[laneChatNoTask], tallies[laneChatRepeat]
	diff := func(what string, a, b, a2 int) {
		d, noise := a-b, abs(a-a2)
		verdict := "в пределах шума — не засчитывается"
		if abs(d) > noise {
			verdict = "больше шума"
		}
		r.metric(what, laneChatDiff, "%+d (шум ±%d — |основная − повтор|): %s", d, noise, verdict)
	}
	diff("разница: цель названа на контрольных", m.goal, no.goal, rep.goal)
	diff("разница: нарушений ограничений", m.violations, no.violations, rep.violations)
	diff("разница: ходов с показанными источниками", m.shown, no.shown, rep.shown)
}

// samples — 3–4 хода: последняя контрольная реплика первого сценария на
// основной дорожке и без памяти задачи, вопрос вне базы, смена
// ограничения посередине разговора.
func (t *Chat) samples(r *Result, scs []dialogs.Scenario, res map[string]*chatLane) {
	add := func(lane string, si, n int, topic string) {
		x := res[lane]
		if x == nil || si >= len(x.plays) {
			return
		}
		for _, tr := range x.plays[si].rep.Turns {
			if tr.N == n {
				r.Samples = append(r.Samples, Sample{Topic: topic, Lane: lane, User: tr.Text, Reply: tr.Observed.Reply,
					Note: chatChecksLine(tr.Checks)})
			}
		}
	}
	for si, sc := range scs {
		if si == 0 {
			if n := lastMarked(sc, dialogs.MarkGoal); n > 0 {
				topic := fmt.Sprintf("%s·%d контрольная реплика", sc.ID, n)
				add(laneChatTask, si, n, topic)
				add(laneChatNoTask, si, n, topic)
			}
		}
		for i, turn := range sc.Turns {
			if turn.Unknown {
				add(laneChatTask, si, i+1, fmt.Sprintf("%s·%d вопрос вне базы", sc.ID, i+1))
				break
			}
		}
		for i, turn := range sc.Turns {
			if i > 0 && turn.Has(dialogs.MarkConstraint) {
				add(laneChatTask, si, i+1, fmt.Sprintf("%s·%d смена ограничения", sc.ID, i+1))
				break
			}
		}
	}
	if len(r.Samples) > 4 {
		r.Samples = r.Samples[:4]
	}
}

func lastMarked(sc dialogs.Scenario, mark string) int {
	n := 0
	for i, turn := range sc.Turns {
		if turn.Has(mark) {
			n = i + 1
		}
	}
	return n
}

// chatChecksLine — проверки хода одной строкой: [+] пройдена, [-] нет,
// [~] не определить (как у kb chat).
func chatChecksLine(cs []dialogs.Check) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		mark := "[+]"
		switch {
		case c.NA:
			mark = "[~]"
		case !c.OK:
			mark = "[-]"
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s)", mark, c.Name, c.Got))
	}
	return strings.Join(parts, "; ")
}
