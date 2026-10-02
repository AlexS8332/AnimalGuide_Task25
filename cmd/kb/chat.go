package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/dialogs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/history"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/rag"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/runs"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/task"
)

func init() {
	register("chat", "мини-чат со справочником в терминале: REST запущенного приложения, ответ с источниками и цитатами; -script — прогон длинного сценария с проверками", runChat)
}

// defaultChatURL — адрес приложения по умолчанию (go run . слушает его же).
const defaultChatURL = "http://127.0.0.1:8770"

// stdin — откуда REPL читает реплики (подменяется в тестах).
var stdin io.Reader = os.Stdin

// maxQuoteRunes — до скольких символов сокращать цитату в выводе.
const maxQuoteRunes = 140

func runChat(ctx context.Context, args []string, out, errOut io.Writer) int {
	fs := newFlagSet("chat", "[флаги]", errOut)
	base := fs.String("url", defaultChatURL, "адрес запущенного приложения (go run . -preset rag)")
	id := fs.String("id", "", "продолжить диалог с этим идентификатором; пусто — новый диалог")
	preset := fs.String("preset", features.PresetRAG, "пресет механизмов нового диалога («rag» — справочная по базе); пусто — умолчания приложения")
	spec := fs.String("features", "", "механизмы нового диалога поверх пресета: «-guard», «+mcp»")
	script := fs.String("script", "", "сценарий (eval/dialogs/a.json): прогнать реплики без интерактива и проверить ответы")
	trace := fs.Bool("json", false, "трасса: по строке JSON на ход (ход из истории целиком, с журналом и проверками) в stdout; человекочитаемый вывод — в stderr")
	corpusDir := fs.String("corpus", "corpus", "каталог корпуса для сверки doc_id сценария; нет каталога — без сверки")
	timeout := fs.Duration("timeout", 10*time.Minute, "предел одного хода")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	log := out
	if *trace {
		log = errOut
	}
	c := &chatClient{base: strings.TrimRight(*base, "/"), http: &http.Client{}, turnTimeout: *timeout,
		preset: *preset, features: *spec, id: *id}

	var sc *dialogs.Scenario
	if *script != "" {
		s, err := dialogs.Load(*script)
		if err != nil {
			fmt.Fprintln(errOut, "ошибка:", err)
			return exitUsage
		}
		docs, _ := dialogs.CorpusDocs(*corpusDir)
		if err := dialogs.Validate(s, docs); err != nil {
			fmt.Fprintln(errOut, "ошибка:", err)
			return exitUsage
		}
		if !flagSet(fs, "preset") && s.Preset != "" {
			c.preset = s.Preset
		}
		sc = &s
	}
	if c.id == "" || sc != nil {
		if err := c.create(ctx, titleOf(sc)); err != nil {
			fmt.Fprintln(errOut, "ошибка:", err)
			return exitFailed
		}
	}
	fmt.Fprintf(log, "Диалог %s на %s (пресет %s)\n", c.id, c.base, orDash(c.preset))
	if sc != nil {
		return playScript(ctx, c, *sc, log, out, *trace)
	}
	return repl(ctx, c, log, out, *trace)
}

// flagSet — задан ли флаг явно.
func flagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) { found = found || f.Name == name })
	return found
}

func titleOf(s *dialogs.Scenario) string {
	if s == nil {
		return "kb chat"
	}
	return "сценарий " + s.ID + ": " + s.Title
}

// repl — интерактивный чат: реплика — ход, /new, /task, /quit.
func repl(ctx context.Context, c *chatClient, log, out io.Writer, trace bool) int {
	fmt.Fprintln(log, "Команды: /new — новый диалог, /task — память задачи, /quit — выход.")
	in := bufio.NewScanner(stdin)
	in.Buffer(make([]byte, 64<<10), 1<<20)
	n := 0
	for {
		fmt.Fprint(log, "> ")
		if !in.Scan() {
			fmt.Fprintln(log)
			return exitOK
		}
		line := strings.TrimSpace(in.Text())
		switch {
		case line == "":
			continue
		case line == "/quit" || line == "/exit":
			return exitOK
		case line == "/new":
			if err := c.create(ctx, "kb chat"); err != nil {
				fmt.Fprintln(log, "ошибка:", err)
				continue
			}
			n = 0
			fmt.Fprintf(log, "Новый диалог %s (пресет %s)\n", c.id, orDash(c.preset))
			continue
		case line == "/task":
			printTask(ctx, c, log)
			continue
		case line == "/help":
			fmt.Fprintln(log, "Команды: /new — новый диалог, /task — память задачи, /quit — выход.")
			continue
		case strings.HasPrefix(line, "/"):
			fmt.Fprintf(log, "нет команды %s; /help — список\n", line)
			continue
		}
		o, err := c.Ask(ctx, line)
		if err != nil {
			fmt.Fprintln(log, "ошибка:", err)
			if ctx.Err() != nil {
				return exitFailed
			}
			continue
		}
		n++
		printTurn(log, o, c.last)
		if trace {
			writeTrace(out, traceLine{N: n, Text: line, Observed: o, Turn: c.last})
		}
	}
}

// playScript — прогон сценария: реплики по порядку, ответ и проверки по
// каждому ходу, итог. Код выхода 1 — хоть одна проверка не пройдена.
func playScript(ctx context.Context, c *chatClient, s dialogs.Scenario, log, out io.Writer, trace bool) int {
	fmt.Fprintf(log, "Сценарий %s — %s: %d реплик\nЦель: %s\n", s.ID, s.Title, len(s.Turns), s.Goal.Text)
	rep, err := dialogs.Play(ctx, s, c, func(tr dialogs.TurnReport) {
		fmt.Fprintf(log, "\n── %d/%d", tr.N, len(s.Turns))
		if len(tr.Marks) > 0 {
			fmt.Fprintf(log, " [%s]", strings.Join(tr.Marks, ", "))
		}
		fmt.Fprintf(log, " ──\nЧеловек: %s\n", tr.Text)
		printTurn(log, tr.Observed, c.last)
		fmt.Fprintln(log, "Проверки: "+checksLine(tr.Checks))
		if trace {
			writeTrace(out, traceLine{Scenario: s.ID, N: tr.N, Text: tr.Text, Marks: tr.Marks, Observed: tr.Observed, Checks: tr.Checks, Turn: c.last})
		}
	})
	fmt.Fprintln(log)
	if err != nil {
		fmt.Fprintln(log, "прогон оборван:", err)
	}
	fmt.Fprintln(log, rep.Summary())
	for _, tr := range rep.Turns {
		for _, f := range tr.Failed() {
			fmt.Fprintf(log, "  ход %d: %s — ждали %s, получили %s\n", tr.N, f.Name, f.Want, f.Got)
		}
	}
	if trace {
		writeTrace(out, map[string]any{"scenario": s.ID, "summary": rep.Summary(), "passed": rep.Passed() && err == nil, "tallies": rep.Tallies()})
	}
	if err != nil || !rep.Passed() {
		return exitFailed
	}
	return exitOK
}

func checksLine(cs []dialogs.Check) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		mark := "[+]"
		switch {
		case c.NA:
			mark = "[~]"
		case !c.OK && c.Soft:
			// Отчётная проверка (must, grounded): ход не валит.
			mark = "[!]"
		case !c.OK:
			mark = "[-]"
		}
		name := c.Name
		if c.Soft {
			name += ", отчётно"
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s)", mark, name, c.Got))
	}
	return strings.Join(parts, "; ")
}

// traceLine — строка трассы -json.
type traceLine struct {
	Scenario string           `json:"scenario,omitempty"`
	N        int              `json:"n"`
	Text     string           `json:"text"`
	Marks    []string         `json:"marks,omitempty"`
	Observed dialogs.Observed `json:"observed"`
	Checks   []dialogs.Check  `json:"checks,omitempty"`
	Turn     *history.Turn    `json:"turn,omitempty"`
}

func writeTrace(w io.Writer, v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(w, "%s\n", data)
}

// printTurn — ответ хода для человека: текст, источники, цитаты кратко,
// «не знаю» и уточнение, правки памяти задачи, цена хода.
func printTurn(w io.Writer, o dialogs.Observed, t *history.Turn) {
	if o.Error != "" {
		fmt.Fprintln(w, "Ход не удался: "+o.Error)
		return
	}
	v := o.Cite
	switch {
	case v == nil:
		fmt.Fprintln(w, "Справочник: "+strings.TrimSpace(o.Reply))
		fmt.Fprintf(w, "Источники: нет — ответ не из базы знаний (маршрут %s; нужен механизм rag.cite: -preset rag)\n", orDash(o.Route))
	case v.Meta:
		fmt.Fprintln(w, "Справочник: "+strings.TrimSpace(o.Reply))
		fmt.Fprintln(w, "Источники: "+orDash(v.MetaSource))
	case v.Cited.Unknown():
		fmt.Fprintln(w, "Справочник: Не знаю — "+strings.TrimSpace(v.Cited.Answer))
		if q := strings.TrimSpace(v.Cited.Clarify); q != "" {
			fmt.Fprintln(w, "Уточните: "+q)
		}
		printSources(w, "Ближайшее в базе", v.Sources)
	default:
		fmt.Fprintln(w, "Справочник: "+strings.TrimSpace(v.Cited.Answer))
		printSources(w, "Источники", v.Sources)
		if len(v.Cited.Quotes) > 0 {
			fmt.Fprintln(w, "Цитаты:")
			num := map[string]int{}
			for _, s := range v.Sources {
				num[s.ChunkID] = s.N
			}
			for _, q := range v.Cited.Quotes {
				fmt.Fprintf(w, "  «%s» [%d]\n", short(q.Text, maxQuoteRunes), num[q.ChunkID])
			}
		}
		if v.Check.Unverified {
			fmt.Fprintln(w, "Проверка кодом: НЕ проверено — "+strings.Join(v.Check.Problems, "; "))
		}
		// Принят без опоры (мягкая проверка чисел, вид не из источника):
		// человек видит, что именно цитатами не подтверждено.
		if !v.Check.Grounded {
			if len(v.Check.NumbersMissing) > 0 {
				fmt.Fprintln(w, "⚠ числа без цитаты: "+strings.Join(v.Check.NumbersMissing, ", "))
			}
			if len(v.Check.SpeciesMismatch) > 0 {
				fmt.Fprintln(w, "⚠ вид не из источника: "+strings.Join(v.Check.SpeciesMismatch, ", "))
			}
		}
	}
	if t == nil {
		return
	}
	if line := taskChanges(t); line != "" {
		fmt.Fprintln(w, "Задача: "+line)
	}
	tot := t.Totals
	cost := "цена неизвестна"
	if tot.Cost.Known {
		cost = fmt.Sprintf("$%.4f", tot.Cost.USD)
	}
	fmt.Fprintf(w, "Ход: маршрут %s, запросов к модели %d, токенов %d (кэш %d), %s, %.1f с\n",
		orDash(t.Route), tot.LLMCalls, tot.Usage.Total, tot.Usage.CacheHit, cost, tot.Seconds)
}

func printSources(w io.Writer, head string, srcs []rag.CiteViewSrc) {
	if len(srcs) == 0 {
		fmt.Fprintln(w, head+": нет")
		return
	}
	fmt.Fprintln(w, head+":")
	for _, s := range srcs {
		title := s.ChunkID
		if s.Title != "" {
			title = s.Title
			if s.Path != "" {
				title += " › " + s.Path
			}
			title += " (" + s.ChunkID + ")"
		}
		if s.Past {
			title += " — из прошлого хода"
		}
		fmt.Fprintf(w, "  [%d] %s\n", s.N, title)
	}
}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// taskChanges — правки памяти задачи за ход (итоги механизма task:
// список task.Change или объект с полем changes). Формата нет — пусто.
func taskChanges(t *history.Turn) string {
	raw, ok := t.Extras[string(features.Task)]
	if !ok {
		return ""
	}
	var list []task.Change
	if json.Unmarshal(raw, &list) != nil {
		var obj struct {
			Changes []task.Change `json:"changes"`
		}
		if json.Unmarshal(raw, &obj) != nil {
			return ""
		}
		list = obj.Changes
	}
	parts := make([]string, 0, len(list))
	for _, c := range list {
		p := c.Op + " " + c.List + " «" + c.Text + "»"
		if c.Reason != "" {
			p += " (" + c.Reason + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

// printTask — память задачи диалога: REST панели «Задача»
// (GET /api/task/{id}, internal/taskapi) — состояние текущей ветки.
func printTask(ctx context.Context, c *chatClient, w io.Writer) {
	var res struct {
		Task task.State `json:"task"`
		On   bool       `json:"on"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/task/"+url.PathEscape(c.id), nil, &res); err != nil {
		fmt.Fprintln(w, "ошибка:", err)
		return
	}
	if !res.On {
		fmt.Fprintln(w, "Механизм «Память задачи» в этом диалоге выключен.")
	}
	st := res.Task
	if st.Empty() {
		fmt.Fprintln(w, "Память задачи пуста: цель ещё не названа.")
		return
	}
	fmt.Fprintln(w, "Цель: "+orDash(st.Goal))
	list := func(head string, items []task.Item) {
		for _, it := range items {
			fmt.Fprintf(w, "%s: %s\n", head, it.Text)
		}
	}
	list("Уточнено", st.Clarified)
	list("Ограничение", st.Constraints)
	for _, t := range st.Terms {
		fmt.Fprintf(w, "Термин: %s — %s\n", t.Term, t.Meaning)
	}
	list("Открыто", st.Open)
}

// chatClient — клиент REST приложения: диалог, ход, ожидание по потоку
// событий хода, итог хода из истории диалога.
type chatClient struct {
	base        string
	http        *http.Client
	turnTimeout time.Duration
	preset      string
	features    string
	id          string
	// last — последний ход из истории диалога (журнал, итоги механизмов).
	last *history.Turn
}

var _ dialogs.Asker = (*chatClient)(nil)

// do — запрос с JSON-телом; ответ не 2xx — ошибка с текстом сервера.
func (c *chatClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("приложение не отвечает на %s (запустите go run . -preset rag или укажите -url): %w", c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s %s: %d — %s", method, path, resp.StatusCode, e.Error)
		}
		return fmt.Errorf("%s %s: %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// create — новый пустой диалог с пресетом и механизмами клиента.
func (c *chatClient) create(ctx context.Context, title string) error {
	var res struct {
		Conversation struct {
			ID string `json:"id"`
		} `json:"conversation"`
	}
	body := map[string]any{"empty": true, "preset": c.preset, "features": c.features, "title": title}
	if err := c.do(ctx, http.MethodPost, "/api/conversations", body, &res); err != nil {
		return err
	}
	if res.Conversation.ID == "" {
		return errors.New("сервер не вернул идентификатор диалога")
	}
	c.id = res.Conversation.ID
	return nil
}

// convDetail — то, что клиенту нужно из карточки диалога.
type convDetail struct {
	TurnList []history.Turn             `json:"turnList"`
	Extras   map[string]json.RawMessage `json:"extras"`
}

func (c *chatClient) detail(ctx context.Context) (convDetail, error) {
	var res struct {
		Conversation convDetail `json:"conversation"`
	}
	err := c.do(ctx, http.MethodGet, "/api/conversations/"+url.PathEscape(c.id), nil, &res)
	return res.Conversation, err
}

// Ask — ход: реплика, ожидание конца хода, итог из истории диалога.
func (c *chatClient) Ask(ctx context.Context, text string) (dialogs.Observed, error) {
	if c.turnTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.turnTimeout)
		defer cancel()
	}
	var started struct {
		Turn runs.View `json:"turn"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/conversations/"+url.PathEscape(c.id)+"/turns", map[string]string{"text": text}, &started); err != nil {
		return dialogs.Observed{}, err
	}
	view, err := c.wait(ctx, started.Turn.ID)
	if err != nil {
		return dialogs.Observed{}, err
	}
	o := dialogs.Observed{TurnID: view.ID, Route: view.Route, Reply: view.Reply, Error: view.Error}
	c.last = nil
	d, err := c.detail(ctx)
	if err != nil {
		return o, err
	}
	for i := range d.TurnList {
		if d.TurnList[i].ID == view.ID {
			t := d.TurnList[i]
			c.last = &t
			break
		}
	}
	if c.last != nil {
		var v rag.CiteView
		if c.last.Extra(string(features.RAGCite), &v) {
			o.Cite = &v
			// Опора на цитаты — из проверки kb_answer (rag.CiteCheck.Grounded);
			// у «не знаю» и реплик о разговоре её нет.
			if !v.Meta && v.Cited.Status != "" {
				g := v.Check.Grounded
				o.Grounded = &g
			}
		}
		if o.Reply == "" {
			o.Reply = c.last.Reply
		}
	}
	return o, nil
}

// wait — конец хода по потоку событий (/api/turns/{id}/events, событие
// done); поток оборвался раньше — опрос /api/turns/{id}.
func (c *chatClient) wait(ctx context.Context, turnID string) (runs.View, error) {
	path := "/api/turns/" + url.PathEscape(turnID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"/events", nil)
	if err != nil {
		return runs.View{}, err
	}
	if resp, err := c.http.Do(req); err == nil {
		v, done := readDone(resp.Body)
		resp.Body.Close()
		if done {
			return v, nil
		}
	}
	for {
		var res struct {
			Turn runs.View `json:"turn"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &res); err != nil {
			return runs.View{}, err
		}
		if res.Turn.Status != runs.StatusRunning {
			return res.Turn, nil
		}
		select {
		case <-ctx.Done():
			return runs.View{}, fmt.Errorf("ход не закончился: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// readDone читает поток SSE до события done.
func readDone(r io.Reader) (runs.View, bool) {
	br := bufio.NewReaderSize(r, 64<<10)
	event := ""
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && event == "done":
			var v runs.View
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v) == nil {
				return v, true
			}
		case line == "":
			event = ""
		}
		if err != nil {
			return runs.View{}, false
		}
	}
}
