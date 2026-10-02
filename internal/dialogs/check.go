package dialogs

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/features"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/history"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/profile"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/rag"
)

// Observed — что ход дал человеку: текст ответа и итог механизма rag.cite
// (Extras хода, ключ rag.cite). Cite == nil — механизма на ходе не было
// (ход ушёл мимо ведущего или rag.cite выключен).
type Observed struct {
	TurnID string        `json:"turn_id,omitempty"`
	Route  string        `json:"route,omitempty"`
	Reply  string        `json:"reply"`
	Error  string        `json:"error,omitempty"`
	Cite   *rag.CiteView `json:"cite,omitempty"`
	// Grounded — признак опоры ответа на цитаты от проверки rag.cite
	// (check.grounded итога хода), если она его даёт; nil — нет, и опора
	// считается по NumbersMissing и SpeciesMismatch (IsGrounded).
	Grounded *bool `json:"grounded,omitempty"`
}

// FromTurn — наблюдение из записи хода в истории диалога: то же, что kb
// chat собирает по REST (ответ, ошибка, итог rag.cite), но без сервера —
// для стенда, который ходит в менеджер ходов напрямую. Незавершённый ход
// без текста ошибки — ошибка со статусом хода.
func FromTurn(t history.Turn) Observed {
	o := Observed{TurnID: t.ID, Route: t.Route, Reply: t.Reply, Error: t.Error}
	if t.Status != history.TurnDone && strings.TrimSpace(o.Error) == "" {
		o.Error = "ход не завершён (" + orDash(t.Status) + ")"
	}
	var v rag.CiteView
	if t.Extra(string(features.RAGCite), &v) {
		o.Cite = &v
		var g struct {
			Check struct {
				Grounded *bool `json:"grounded"`
			} `json:"check"`
		}
		if t.Extra(string(features.RAGCite), &g) {
			o.Grounded = g.Check.Grounded
		}
	}
	return o
}

// IsGrounded — ответ по базе опирается на свои цитаты: признак проверки
// rag.cite (Observed.Grounded), а без него — у ответа нет чисел вне цитат
// (NumbersMissing) и видов без цитаты о них (SpeciesMismatch). Второе
// значение — что не так.
func (o Observed) IsGrounded() (bool, string) {
	if o.Cite == nil {
		return false, "нет итога rag.cite"
	}
	if o.Grounded != nil {
		if *o.Grounded {
			return true, "опирается"
		}
		return false, "проверка: не опирается"
	}
	var why []string
	if len(o.Cite.Check.NumbersMissing) > 0 {
		why = append(why, "чисел нет в цитатах: "+strings.Join(o.Cite.Check.NumbersMissing, ", "))
	}
	if len(o.Cite.Check.SpeciesMismatch) > 0 {
		why = append(why, "виды без цитаты о них: "+strings.Join(o.Cite.Check.SpeciesMismatch, ", "))
	}
	if len(why) > 0 {
		return false, strings.Join(why, "; ")
	}
	return true, "опирается"
}

// SourcesShown — ответ показал источники так, как велит ТЗ (раздел 10):
// по базе — со списком источников; «не знаю» — с ближайшим найденным или
// с пустой выдачей (честно пустой список — тоже показ); о разговоре —
// источник «память задачи». Не путать с проверкой sources: та ещё и
// сверяет, ждали ли здесь ответа по базе.
func SourcesShown(o Observed) (bool, string) {
	switch st := o.Status(); st {
	case "answered":
		return len(o.Cite.Sources) > 0 && o.Cite.Check.HasSources, fmt.Sprintf("answered, источников %d", len(o.Cite.Sources))
	case "unknown":
		return true, fmt.Sprintf("unknown, ближайших %d", len(o.Cite.Sources))
	case "meta":
		return true, "meta: " + orDash(o.Cite.MetaSource)
	default:
		return false, st
	}
}

// Status — как ответил ход: answered (по базе), unknown («не знаю»), meta
// (по памяти задачи), none (без rag.cite), error.
func (o Observed) Status() string {
	switch {
	case o.Error != "":
		return "error"
	case o.Cite == nil:
		return "none"
	case o.Cite.Meta:
		return "meta"
	case o.Cite.Cited.Unknown():
		return "unknown"
	}
	return "answered"
}

// Docs — doc_id источников ответа (chunk_id — «doc/стратегия/номер»), по
// порядку, без повторов.
func (o Observed) Docs() []string {
	if o.Cite == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range o.Cite.Sources {
		d, _, _ := strings.Cut(s.ChunkID, "/")
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// answer — что мерить ограничениями: ответ без обвязки «Источники/Цитаты»
// (латынь в цитате из MDD — не нарушение «без латыни» в ответе).
func (o Observed) answer() string {
	if o.Cite != nil && !o.Cite.Meta {
		return o.Cite.Cited.EvalText()
	}
	return o.Reply
}

// Check — одна проверка хода. NA — «не определить» (в знаменатель не идёт).
// Soft — отчётная: в итогах считается, но провалом хода не делает (must,
// grounded — признаки качества ответа, а не договорённости разговора).
type Check struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	NA   bool   `json:"na,omitempty"`
	Soft bool   `json:"soft,omitempty"`
	Want string `json:"want,omitempty"`
	Got  string `json:"got,omitempty"`
}

// Имена проверок.
const (
	CheckReply   = "reply"   // ход завершился ответом
	CheckSources = "sources" // ответ с источниками (или «не знаю», где ждали; память задачи на контрольной)
	CheckDocs    = "docs"    // ожидаемые doc_id среди источников
	CheckGoal    = "goal"    // контрольная реплика: цель названа
	CheckMust    = "must"    // в ответе есть обязательные числа и слова реплики
	// CheckGrounded — ответ по базе опирается на цитаты (IsGrounded); у
	// «не знаю» и ответа о разговоре не определить.
	CheckGrounded = "grounded"
)

// latinRe — латинский бином («Lynx lynx»); тот же признак, что
// bench.HasLatin (bench сам импортирует dialogs для испытания, поэтому
// здесь копия, а не импорт).
var latinRe = regexp.MustCompile(`\b[A-Z][a-z]{2,}\s[a-z]{3,}\b`)

// iucnRe — статус МСОП: код категории (заглавными) или её русское
// название.
var iucnRe = regexp.MustCompile(`\b(LC|NT|VU|EN|CR|EW|EX|DD|NE)\b|(?i:вызывающ\p{L}* наименьш|уязвим|вымирающ|исчезающ|под угрозой|в опасности|недостаточно данных|не оценивал)`)

// latinWordRe — слово латиницей от четырёх букв: род, семейство
// («Felidae», «Martes», «Mustelinae»), одиночный эпитет.
var latinWordRe = regexp.MustCompile(`\b[A-Za-z]{4,}\b`)

// HasLatin — есть ли в тексте латинский бином.
func HasLatin(text string) bool { return latinRe.MatchString(text) }

// LatinFound — первое латинское название в тексте для проверки «без
// латыни»: бином или одиночное слово латиницей от четырёх букв. Не латынь —
// аббревиатуры: коды МСОП (LC, NT, VU, EN, CR, EW, EX, DD, NE) и «MDD»
// короче четырёх букв, а «IUCN», «GBIF» целиком заглавные.
func LatinFound(text string) string {
	if m := latinRe.FindString(text); m != "" {
		return m
	}
	for _, w := range latinWordRe.FindAllString(text, -1) {
		if strings.ToUpper(w) != w {
			return w
		}
	}
	return ""
}

// numberRe — число в тексте: «8», «0,8», «1.4», «58».
var numberRe = regexp.MustCompile(`\d+(?:[.,]\d+)?`)

// MustMissing — какие обязательные пункты не нашлись в тексте: число — среди
// чисел текста («0,8» и «0.8» — одно число, «8» не находится в «58»),
// слово — по началу слова без учёта регистра и ё.
func MustMissing(text string, must []string) []string {
	nums := map[string]bool{}
	for _, n := range numberRe.FindAllString(text, -1) {
		nums[strings.ReplaceAll(n, ",", ".")] = true
	}
	var out []string
	for _, m := range must {
		m = strings.TrimSpace(m)
		if m != "" && numberRe.FindString(m) == m {
			if !nums[strings.ReplaceAll(m, ",", ".")] {
				out = append(out, m)
			}
			continue
		}
		if len(GoalNamed(text, []string{m})) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// HasIUCN — назван ли статус МСОП.
func HasIUCN(text string) bool { return iucnRe.MatchString(text) }

// Sentences — число предложений (тот же счёт, что у проверки профиля).
func Sentences(text string) int { return profile.Measure(text).Sentences }

// GoalNamed — какие группы ключевых слов цели не нашлись в тексте (пусто —
// цель названа). Группа «доклад|сообщени» — альтернативы; слово ищется по
// началу слова, без учёта регистра и ё.
func GoalNamed(text string, keywords []string) (missing []string) {
	words := strings.FieldsFunc(norm(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, group := range keywords {
		found := false
		for _, alt := range strings.Split(group, "|") {
			alt = norm(strings.TrimSpace(alt))
			for _, w := range words {
				if alt != "" && strings.HasPrefix(w, alt) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			missing = append(missing, group)
		}
	}
	return missing
}

func norm(s string) string { return strings.ReplaceAll(strings.ToLower(s), "ё", "е") }

// CheckTurn — проверки хода n (с 1) сценария по тому, что ход дал.
func CheckTurn(s Scenario, n int, o Observed) []Check {
	t := s.Turns[n-1]
	st := o.Status()
	out := []Check{{Name: CheckReply, OK: st != "error" && strings.TrimSpace(o.Reply) != "", Want: "ответ", Got: orDash(o.Error)}}
	if st == "error" {
		return out
	}

	// Источники: ответ по базе — со списком источников; вопрос вне базы —
	// «не знаю»; контрольная реплика — память задачи.
	src := Check{Name: CheckSources}
	switch {
	case t.Has(MarkGoal):
		src.Want = "память задачи (meta) или источники из базы"
		src.OK = st == "meta" || (st == "answered" && len(o.Cite.Sources) > 0)
	case t.Unknown:
		src.Want = "«не знаю» (вопрос вне базы)"
		src.OK = st == "unknown"
	default:
		src.Want = "ответ по базе с источниками"
		src.OK = st == "answered" && len(o.Cite.Sources) > 0 && o.Cite.Check.HasSources
	}
	src.Got = st
	if o.Cite != nil && len(o.Cite.Sources) > 0 {
		src.Got += fmt.Sprintf(", источников %d", len(o.Cite.Sources))
	}
	out = append(out, src)

	// Ожидаемые doc_id.
	if len(t.Docs) > 0 {
		got := o.Docs()
		var missing []string
		for _, want := range t.Docs {
			hit := false
			for _, alt := range strings.Split(want, "|") {
				for _, d := range got {
					hit = hit || d == strings.TrimSpace(alt)
				}
			}
			if !hit {
				missing = append(missing, want)
			}
		}
		c := Check{Name: CheckDocs, OK: len(missing) == 0, Want: strings.Join(t.Docs, ", "), Got: orDash(strings.Join(got, ", "))}
		if len(missing) > 0 {
			c.Got += "; нет: " + strings.Join(missing, ", ")
		}
		out = append(out, c)
	}

	// Обязательные числа и слова — в самом ответе, без цитат: число,
	// которое стоит только в цитате, человек ответом не прочтёт.
	if len(t.Must) > 0 {
		missing := MustMissing(o.answer(), t.Must)
		c := Check{Name: CheckMust, Soft: true, OK: len(missing) == 0, Want: strings.Join(t.Must, ", "), Got: "все"}
		if len(missing) > 0 {
			c.Got = "нет: " + strings.Join(missing, ", ")
		}
		out = append(out, c)
	}

	// Опора на цитаты — у ответа по базе.
	g := Check{Name: CheckGrounded, Soft: true, Want: "числа и виды ответа — из цитат"}
	if st == "answered" {
		g.OK, g.Got = o.IsGrounded()
	} else {
		g.NA, g.Got = true, "не определить: "+st
	}
	out = append(out, g)

	// Ограничения ответа. Требования «есть латынь», «есть статус» на «не
	// знаю» и на контрольной реплике не определить; запреты и длина — на
	// любом ответе.
	text := o.answer()
	for _, r := range s.Rules {
		if !r.Applies(n) {
			continue
		}
		c := Check{Name: r.Kind}
		presence := r.Kind == RuleLatin || r.Kind == RuleIUCN
		if presence && (st == "unknown" || st == "meta" || t.Has(MarkGoal)) {
			c.NA, c.Got = true, "не определить: "+st
			out = append(out, c)
			continue
		}
		switch r.Kind {
		case RuleNoLatin:
			m := LatinFound(text)
			c.OK, c.Want, c.Got = m == "", "без латыни", orDash(m)
		case RuleLatin:
			c.OK, c.Want, c.Got = HasLatin(text), "латинское название", yes(HasLatin(text))
		case RuleIUCN:
			c.OK, c.Want, c.Got = HasIUCN(text), "статус МСОП", yes(HasIUCN(text))
		case RuleMaxSentences:
			k := Sentences(text)
			c.OK, c.Want, c.Got = k <= r.N, fmt.Sprintf("не больше %d предложений", r.N), fmt.Sprint(k)
		}
		out = append(out, c)
	}

	// Контрольная реплика: цель названа.
	if t.Has(MarkGoal) {
		missing := GoalNamed(o.Reply, s.Goal.Keywords)
		c := Check{Name: CheckGoal, OK: len(missing) == 0, Want: strings.Join(s.Goal.Keywords, " + ")}
		c.Got = "названа"
		if len(missing) > 0 {
			c.Got = "нет: " + strings.Join(missing, ", ")
		}
		out = append(out, c)
	}
	return out
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func yes(b bool) string {
	if b {
		return "есть"
	}
	return "нет"
}

// Asker — собеседник сценария: отправляет реплику в диалог и возвращает,
// что ход дал. Ошибка — прогон не может продолжаться (сеть, сервер);
// неудачный ход — Observed.Error.
type Asker interface {
	Ask(ctx context.Context, text string) (Observed, error)
}

// TurnReport — ход сценария с проверками.
type TurnReport struct {
	N        int      `json:"n"`
	Text     string   `json:"text"`
	Marks    []string `json:"marks,omitempty"`
	Observed Observed `json:"observed"`
	Checks   []Check  `json:"checks"`
}

// Failed — непройденные проверки хода (отчётные — не в счёт).
func (t TurnReport) Failed() []Check {
	var out []Check
	for _, c := range t.Checks {
		if !c.OK && !c.NA && !c.Soft {
			out = append(out, c)
		}
	}
	return out
}

// Report — прогон сценария.
type Report struct {
	Scenario string       `json:"scenario"`
	Title    string       `json:"title"`
	Turns    []TurnReport `json:"turns"`
}

// Play — прогон сценария: реплики по порядку, после каждой — проверки;
// onTurn получает ход сразу (журнал по ходу прогона). Ошибка Asker
// обрывает прогон: отчёт — о сыгранных ходах.
func Play(ctx context.Context, s Scenario, a Asker, onTurn func(TurnReport)) (Report, error) {
	rep := Report{Scenario: s.ID, Title: s.Title}
	for i, t := range s.Turns {
		o, err := a.Ask(ctx, t.Text)
		if err != nil {
			return rep, fmt.Errorf("реплика %d: %w", i+1, err)
		}
		tr := TurnReport{N: i + 1, Text: t.Text, Marks: t.Marks, Observed: o, Checks: CheckTurn(s, i+1, o)}
		rep.Turns = append(rep.Turns, tr)
		if onTurn != nil {
			onTurn(tr)
		}
	}
	return rep, nil
}

// Tally — пройдено из определимых по имени проверки.
type Tally struct {
	Name  string `json:"name"`
	OK    int    `json:"ok"`
	Total int    `json:"total"`
}

// Tallies — итоги по видам проверок в порядке первого появления.
func (r Report) Tallies() []Tally {
	var out []Tally
	idx := map[string]int{}
	for _, t := range r.Turns {
		for _, c := range t.Checks {
			if c.NA {
				continue
			}
			i, ok := idx[c.Name]
			if !ok {
				i = len(out)
				idx[c.Name] = i
				out = append(out, Tally{Name: c.Name})
			}
			out[i].Total++
			if c.OK {
				out[i].OK++
			}
		}
	}
	return out
}

// Passed — все определимые неотчётные проверки пройдены.
func (r Report) Passed() bool {
	for _, t := range r.Turns {
		if len(t.Failed()) > 0 {
			return false
		}
	}
	return len(r.Turns) > 0
}

// Summary — итог одной строкой: «источники 14/15, docs 10/11, goal 3/3…».
func (r Report) Summary() string {
	var parts []string
	for _, t := range r.Tallies() {
		parts = append(parts, fmt.Sprintf("%s %d/%d", t.Name, t.OK, t.Total))
	}
	verdict := "пройден"
	if !r.Passed() {
		verdict = "НЕ пройден"
	}
	return fmt.Sprintf("сценарий %s (%d ходов) %s: %s", r.Scenario, len(r.Turns), verdict, strings.Join(parts, ", "))
}
