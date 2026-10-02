// Package task — память задачи диалога (v25): цель разговора, что
// пользователь уже уточнил, какие ограничения и термины зафиксированы, что
// осталось открытым.
//
// Зачем отдельный слой. Окно сообщений (8–12 последних) в длинном диалоге
// теряет первые реплики, а цель («готовлю доклад для школьников о кошках
// Азии», «без латыни», «барс — это ирбис») обычно сказана именно там.
// Карточка фактов (facts) хранит факты о животных, а не о задаче; память
// человека (memory.long) переживает диалог, а задача — нет. Поэтому
// владелец состояния — ВЕТКА диалога (как у facts): при ветвлении оно
// копируется снимком точки, и альтернативная ветка может менять цель, не
// трогая основную.
//
// Кто пишет. Извлекатель (internal/extract) — тем же единственным запросом,
// что раскладывает реплику по памяти, профилю и фактам (бюджет ТЗ: не больше
// 4 запросов к модели на ход). У каждого пункта — дословная цитата из
// реплики ЧЕЛОВЕКА (Quote), сверяемая кодом (words.InReply): источник или
// ответ ассистента задачу не меняют (ИП-14).
//
// Где используется. Блок механизма task (features.Task) — в каждом запросе
// ведущего, место — рядом с карточкой фактов (меняется не каждый ход).
// Переписывание запроса к базе (retrieve) получает термины задачи
// («барс = ирбис») и вид из цели для вопросов-продолжений.
//
// Пакет — лист: импортирует только words. Поэтому его можно звать отовсюду
// (history, extract, retrieve, runs) без циклов импорта.
package task

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/words"
)

// ErrNotImplemented — заглушка контракта (оставлена для совместимости
// импортов; реализация полная).
var ErrNotImplemented = errors.New("не реализовано")

// Пределы: состояние уходит в каждый запрос ведущего — оно должно быть
// коротким (ТЗ, бюджет постоянной части).
const (
	MaxItems  = 8   // в каждом списке
	MaxRunes  = 160 // на пункт
	MaxGoal   = 240 // цель
	MaxQuote  = 160
	BlockName = "task"
)

// Списки состояния — значения PatchItem.List и Change.List.
const (
	ListGoal        = "goal"
	ListClarified   = "clarified"
	ListConstraints = "constraints"
	ListTerms       = "terms"
	ListOpen        = "open"
)

// Операции Change.Op.
const (
	OpSetGoal = "set_goal"
	OpAdd     = "add"
	OpRemove  = "remove"
	OpReject  = "reject"
)

// Lists — списки пунктов в порядке блока (цель — отдельно).
var Lists = []string{ListClarified, ListConstraints, ListTerms, ListOpen}

// Item — пункт: что зафиксировано и дословная цитата человека.
type Item struct {
	Text  string `json:"text"`
	Quote string `json:"quote,omitempty"`
	Turn  int    `json:"turn"` // номер хода, где появился
}

// Term — термин диалога: «барс» → «ирбис (снежный барс)».
type Term struct {
	Term    string `json:"term"`
	Meaning string `json:"meaning"`
	Quote   string `json:"quote,omitempty"`
	Turn    int    `json:"turn"`
}

// State — память задачи ветки.
type State struct {
	// Goal — цель диалога одной фразой; GoalQuote — откуда она.
	Goal      string `json:"goal,omitempty"`
	GoalQuote string `json:"goal_quote,omitempty"`
	GoalTurn  int    `json:"goal_turn,omitempty"`
	// Clarified — что пользователь уже уточнил (уровень, объём, интересующие
	// виды, «только Азия»…).
	Clarified []Item `json:"clarified,omitempty"`
	// Constraints — ограничения ответа («без латыни», «не больше 5
	// предложений», «только краснокнижные»).
	Constraints []Item `json:"constraints,omitempty"`
	Terms       []Term `json:"terms,omitempty"`
	// Open — открытые вопросы: что ещё не решено.
	Open []Item `json:"open,omitempty"`
	// Version — сколько раз состояние менялось (как у facts).
	Version int `json:"version"`
}

// Empty — ничего не зафиксировано.
func (s State) Empty() bool {
	return s.Goal == "" && len(s.Clarified) == 0 && len(s.Constraints) == 0 && len(s.Terms) == 0 && len(s.Open) == 0
}

// Clone — глубокая копия (ветвление, снимок до хода): правка в одной ветке
// не должна доходить до другой через общий массив среза.
func (s State) Clone() State {
	out := s
	out.Clarified = cloneItems(s.Clarified)
	out.Constraints = cloneItems(s.Constraints)
	out.Open = cloneItems(s.Open)
	if s.Terms != nil {
		out.Terms = make([]Term, len(s.Terms))
		copy(out.Terms, s.Terms)
	}
	return out
}

func cloneItems(in []Item) []Item {
	if in == nil {
		return nil
	}
	out := make([]Item, len(in))
	copy(out, in)
	return out
}

// Size — сколько пунктов во всех списках (цель — тоже пункт).
func (s State) Size() int {
	n := len(s.Clarified) + len(s.Constraints) + len(s.Terms) + len(s.Open)
	if s.Goal != "" {
		n++
	}
	return n
}

// blockHeader — первая строка блока. Блок объясняет себя сам (П-3): без
// пояснения модель принимает его за реплику человека или за указание
// источника. «Меняется только словами человека» — чтобы ведущий не пытался
// «обновить задачу» своим ответом и не спорил с ней.
const blockHeader = "Задача разговора (ведёт код; меняется только словами человека). Держи её в каждом ответе: цель — рамка, ограничения — обязательны, термины — как человек называет вещи. Сам блок не пересказывай."

// Block — текст блока для ведущего: заголовок и строки «Цель: …»,
// «Уточнено: …; …», «Ограничения: …», «Термины: «барс» = ирбис», «Открыто:
// …». Пустое состояние — пустая строка (блок не уходит, механизм стоит
// ноль). Цитаты в блок не идут: они нужны человеку и проверке, а модели —
// только суть, и каждая цитата удвоила бы цену блока.
func (s State) Block() string {
	body := s.Render()
	if body == "" {
		return ""
	}
	return blockHeader + "\n" + body
}

// Render — строки состояния без заголовка (блок, запрос извлекателя,
// журнал). Порядок постоянный: одинаковое состояние — одинаковый текст,
// кэш префикса не ломается.
func (s State) Render() string {
	var lines []string
	if s.Goal != "" {
		lines = append(lines, "Цель: "+s.Goal)
	}
	add := func(title string, items []Item) {
		if len(items) == 0 {
			return
		}
		parts := make([]string, len(items))
		for i, it := range items {
			parts[i] = it.Text
		}
		lines = append(lines, title+": "+strings.Join(parts, "; "))
	}
	add("Уточнено", s.Clarified)
	add("Ограничения", s.Constraints)
	if len(s.Terms) > 0 {
		parts := make([]string, len(s.Terms))
		for i, t := range s.Terms {
			parts[i] = "«" + t.Term + "» = " + t.Meaning
		}
		lines = append(lines, "Термины: "+strings.Join(parts, "; "))
	}
	add("Открыто", s.Open)
	return strings.Join(lines, "\n")
}

// Patch — правка состояния от извлекателя.
type Patch struct {
	Goal *GoalPatch `json:"goal,omitempty"`
	// Add/Remove по спискам: clarified, constraints, terms, open.
	Add    []PatchItem `json:"add,omitempty"`
	Remove []PatchItem `json:"remove,omitempty"`
}

// Empty — правка ничего не просит.
func (p Patch) Empty() bool { return p.Goal == nil && len(p.Add) == 0 && len(p.Remove) == 0 }

// GoalPatch — новая цель (или уточнение прежней) и цитата.
type GoalPatch struct {
	Text  string `json:"text"`
	Quote string `json:"quote"`
}

// PatchItem — пункт правки. List — clarified | constraints | terms | open
// (в Remove ещё goal — снять цель).
type PatchItem struct {
	List    string `json:"list"`
	Text    string `json:"text,omitempty"`
	Term    string `json:"term,omitempty"`
	Meaning string `json:"meaning,omitempty"`
	Quote   string `json:"quote,omitempty"`
}

// Change — что изменилось (для чипов под ответом и журнала).
type Change struct {
	Op     string `json:"op"`   // set_goal | add | remove | reject
	List   string `json:"list"` // goal | clarified | constraints | terms | open
	Text   string `json:"text"`
	Reason string `json:"reason,omitempty"` // почему отклонено (нет цитаты в реплике…)
	// Old — прежнее значение: цель, которую заменила новая, или прежнее
	// значение термина. Чип показывает «цель: X (была: Y)» — замена цели
	// не должна проходить молча.
	Old   string `json:"old,omitempty"`
	Quote string `json:"quote,omitempty"`
}

// String — правка словами для журнала.
func (c Change) String() string {
	s := c.Op + " " + c.List + ": " + c.Text
	if c.Old != "" {
		s += " (было: " + c.Old + ")"
	}
	if c.Reason != "" {
		s += " — " + c.Reason
	}
	return s
}

// Applied — принятые правки (без reject).
func Applied(cs []Change) []Change {
	var out []Change
	for _, c := range cs {
		if c.Op != OpReject {
			out = append(out, c)
		}
	}
	return out
}

// Summary — правки одной строкой: «цель; +2 ограничения; −1 открытый».
func Summary(cs []Change) string {
	var parts []string
	for _, c := range Applied(cs) {
		switch c.Op {
		case OpSetGoal:
			parts = append(parts, "цель: "+c.Text)
		case OpAdd:
			parts = append(parts, "+"+ListTitle(c.List)+": "+c.Text)
		case OpRemove:
			parts = append(parts, "−"+ListTitle(c.List)+": "+c.Text)
		}
	}
	if len(parts) == 0 {
		if len(cs) > 0 {
			return fmt.Sprintf("правок нет, отклонено %d", len(cs))
		}
		return "правок нет"
	}
	return strings.Join(parts, "; ")
}

// ListTitle — название списка для человека.
func ListTitle(list string) string {
	switch list {
	case ListGoal:
		return "цель"
	case ListClarified:
		return "уточнено"
	case ListConstraints:
		return "ограничение"
	case ListTerms:
		return "термин"
	case ListOpen:
		return "открыто"
	}
	return list
}

// Причины отказа — их видит человек в журнале и в чипе.
const (
	reasonNoQuote   = "нет цитаты: задача меняется только словами человека"
	reasonBadQuote  = "слов цитаты нет в реплике человека этого хода"
	reasonEmpty     = "пустой пункт"
	reasonList      = "неизвестный список"
	reasonNotFound  = "такого пункта в задаче нет"
	reasonSameGoal  = "цель та же"
	reasonOverflow  = "предел %d пунктов в списке: выпал самый старый"
	reasonDuplicate = "уже есть"
)

// Apply применяет правку: цитата каждого пункта должна быть в реплике
// человека этого хода (user) — иначе пункт отклоняется с причиной; пределы
// MaxItems/MaxRunes; дубли не добавляются. Возвращает изменения.
//
// Порядок: сначала Remove, потом цель, потом Add — «я передумал, давай не
// про манула, а про ирбиса» снимает старое и ставит новое одной правкой.
// Version растёт на каждом принятом изменении (как у facts): по ней
// history решает, чей снимок новее.
func (s *State) Apply(p Patch, user string, turn int) []Change {
	var out []Change
	for _, it := range p.Remove {
		out = append(out, s.remove(it, user)...)
	}
	if p.Goal != nil {
		out = append(out, s.setGoal(*p.Goal, user, turn)...)
	}
	for _, it := range p.Add {
		out = append(out, s.add(it, user, turn)...)
	}
	return out
}

// checkQuote — пустая строка: цитата есть и найдена в реплике. Политика
// сверки — короткие слова считаются (keepShort), как у профиля и
// подтверждений подборки: «и без латыни» целиком состоит из коротких слов.
func checkQuote(quote, user string) string {
	if strings.TrimSpace(quote) == "" {
		return reasonNoQuote
	}
	if ok, _ := words.InReply(quote, user, true); !ok {
		return reasonBadQuote
	}
	return ""
}

func (s *State) setGoal(g GoalPatch, user string, turn int) []Change {
	text := clip(clean(g.Text), MaxGoal)
	quote := clip(clean(g.Quote), MaxQuote)
	if text == "" {
		return []Change{{Op: OpReject, List: ListGoal, Reason: reasonEmpty, Quote: quote}}
	}
	if why := checkQuote(quote, user); why != "" {
		return []Change{{Op: OpReject, List: ListGoal, Text: text, Reason: why, Quote: quote}}
	}
	if same(text, s.Goal) {
		return nil
	}
	old := s.Goal
	s.Goal, s.GoalQuote, s.GoalTurn = text, quote, turn
	s.Version++
	return []Change{{Op: OpSetGoal, List: ListGoal, Text: text, Old: old, Quote: quote}}
}

func (s *State) add(it PatchItem, user string, turn int) []Change {
	list := strings.ToLower(strings.TrimSpace(it.List))
	quote := clip(clean(it.Quote), MaxQuote)
	if list == ListTerms {
		term, meaning := clip(clean(it.Term), MaxRunes), clip(clean(it.Meaning), MaxRunes)
		if term == "" || meaning == "" {
			// Модель порой кладёт «барс = ирбис» в text.
			if a, b, ok := splitTerm(it.Text); ok && term == "" && meaning == "" {
				term, meaning = clip(a, MaxRunes), clip(b, MaxRunes)
			}
		}
		label := term + " = " + meaning
		if term == "" || meaning == "" {
			return []Change{{Op: OpReject, List: list, Text: strings.Trim(label, " ="), Reason: reasonEmpty, Quote: quote}}
		}
		if why := checkQuote(quote, user); why != "" {
			return []Change{{Op: OpReject, List: list, Text: label, Reason: why, Quote: quote}}
		}
		for i, t := range s.Terms {
			if same(t.Term, term) {
				if same(t.Meaning, meaning) {
					return nil
				}
				old := t.Meaning
				s.Terms[i] = Term{Term: term, Meaning: meaning, Quote: quote, Turn: turn}
				s.Version++
				return []Change{{Op: OpAdd, List: list, Text: label, Old: t.Term + " = " + old, Quote: quote}}
			}
		}
		out := []Change{{Op: OpAdd, List: list, Text: label, Quote: quote}}
		s.Terms = append(s.Terms, Term{Term: term, Meaning: meaning, Quote: quote, Turn: turn})
		if len(s.Terms) > MaxItems {
			dropped := s.Terms[0]
			s.Terms = s.Terms[1:]
			out = append(out, Change{Op: OpRemove, List: list, Text: dropped.Term + " = " + dropped.Meaning,
				Reason: fmt.Sprintf(reasonOverflow, MaxItems)})
		}
		s.Version++
		return out
	}
	ptr := s.list(list)
	text := clip(clean(it.Text), MaxRunes)
	if ptr == nil {
		return []Change{{Op: OpReject, List: list, Text: text, Reason: reasonList, Quote: quote}}
	}
	if text == "" {
		return []Change{{Op: OpReject, List: list, Reason: reasonEmpty, Quote: quote}}
	}
	if why := checkQuote(quote, user); why != "" {
		return []Change{{Op: OpReject, List: list, Text: text, Reason: why, Quote: quote}}
	}
	for _, x := range *ptr {
		if same(x.Text, text) {
			return nil
		}
	}
	out := []Change{{Op: OpAdd, List: list, Text: text, Quote: quote}}
	*ptr = append(*ptr, Item{Text: text, Quote: quote, Turn: turn})
	if len(*ptr) > MaxItems {
		// Выпадает самый старый: задача — про то, где разговор сейчас, а
		// молча отказать новому пункту значило бы спорить с человеком.
		dropped := (*ptr)[0]
		*ptr = (*ptr)[1:]
		out = append(out, Change{Op: OpRemove, List: list, Text: dropped.Text, Reason: fmt.Sprintf(reasonOverflow, MaxItems)})
	}
	s.Version++
	return out
}

func (s *State) remove(it PatchItem, user string) []Change {
	list := strings.ToLower(strings.TrimSpace(it.List))
	quote := clip(clean(it.Quote), MaxQuote)
	text := clean(it.Text)
	if list == ListTerms && clean(it.Term) != "" {
		text = clean(it.Term)
	}
	if list == ListGoal {
		if s.Goal == "" {
			return []Change{{Op: OpReject, List: list, Text: text, Reason: reasonNotFound, Quote: quote}}
		}
		if why := checkQuote(quote, user); why != "" {
			return []Change{{Op: OpReject, List: list, Text: s.Goal, Reason: why, Quote: quote}}
		}
		old := s.Goal
		s.Goal, s.GoalQuote, s.GoalTurn = "", "", 0
		s.Version++
		return []Change{{Op: OpRemove, List: list, Text: old, Quote: quote}}
	}
	if text == "" {
		return []Change{{Op: OpReject, List: list, Reason: reasonEmpty, Quote: quote}}
	}
	if list == ListTerms {
		i := s.findTerm(text)
		if i < 0 {
			return []Change{{Op: OpReject, List: list, Text: text, Reason: reasonNotFound, Quote: quote}}
		}
		if why := checkQuote(quote, user); why != "" {
			return []Change{{Op: OpReject, List: list, Text: text, Reason: why, Quote: quote}}
		}
		t := s.Terms[i]
		s.Terms = append(s.Terms[:i:i], s.Terms[i+1:]...)
		s.Version++
		return []Change{{Op: OpRemove, List: list, Text: t.Term + " = " + t.Meaning, Quote: quote}}
	}
	ptr := s.list(list)
	if ptr == nil {
		return []Change{{Op: OpReject, List: list, Text: text, Reason: reasonList, Quote: quote}}
	}
	i := findItem(*ptr, text)
	if i < 0 {
		return []Change{{Op: OpReject, List: list, Text: text, Reason: reasonNotFound, Quote: quote}}
	}
	if why := checkQuote(quote, user); why != "" {
		return []Change{{Op: OpReject, List: list, Text: text, Reason: why, Quote: quote}}
	}
	removed := (*ptr)[i]
	*ptr = append((*ptr)[:i:i], (*ptr)[i+1:]...)
	if len(*ptr) == 0 {
		*ptr = nil
	}
	s.Version++
	return []Change{{Op: OpRemove, List: list, Text: removed.Text, Quote: quote}}
}

// list — указатель на список по имени; nil — такого нет.
func (s *State) list(name string) *[]Item {
	switch name {
	case ListClarified:
		return &s.Clarified
	case ListConstraints:
		return &s.Constraints
	case ListOpen:
		return &s.Open
	}
	return nil
}

// findItem — пункт по тексту: сначала точное совпадение (без регистра),
// потом — по основам слов (модель снимает «без латинских названий»,
// записанное как «без латыни»: большинство основ должно совпасть в обе
// стороны, иначе «только Азия» снимало бы «только краснокнижные»).
func findItem(items []Item, text string) int {
	for i, it := range items {
		if same(it.Text, text) {
			return i
		}
	}
	for i, it := range items {
		if near(it.Text, text) {
			return i
		}
	}
	return -1
}

func (s State) findTerm(text string) int {
	if a, _, ok := splitTerm(text); ok {
		text = a
	}
	for i, t := range s.Terms {
		if same(t.Term, text) {
			return i
		}
	}
	for i, t := range s.Terms {
		if near(t.Term, text) {
			return i
		}
	}
	return -1
}

// splitTerm — «барс = ирбис», «барс — ирбис», «барс → ирбис».
func splitTerm(s string) (string, string, bool) {
	for _, sep := range []string{"=", "→", " — ", " - ", ":"} {
		if a, b, ok := strings.Cut(s, sep); ok {
			a, b = clean(strings.Trim(a, "«»\"")), clean(strings.Trim(b, "«»\""))
			if a != "" && b != "" {
				return a, b, true
			}
		}
	}
	return "", "", false
}

// near — тексты совпадают по основам слов в обе стороны (большинство).
func near(a, b string) bool {
	sa, sb := stems(a), stems(b)
	if len(sa) == 0 || len(sb) == 0 {
		return false
	}
	return covered(sa, sb)*2 > len(sa) && covered(sb, sa)*2 > len(sb)
}

func covered(of, in []string) int {
	set := map[string]bool{}
	for _, w := range in {
		set[w] = true
	}
	n := 0
	for _, w := range of {
		if set[w] {
			n++
		}
	}
	return n
}

// Clean — состояние после ручной правки (панель «Задача»): пробелы, пределы
// длины и числа пунктов, пустые и повторы выкинуты. Версию не трогает —
// её ставит тот, кто записывает.
func (s State) Clean() State {
	out := State{Version: s.Version}
	out.Goal = clip(clean(s.Goal), MaxGoal)
	if out.Goal != "" {
		out.GoalQuote, out.GoalTurn = clip(clean(s.GoalQuote), MaxQuote), s.GoalTurn
	}
	cleanList := func(in []Item) []Item {
		var res []Item
		for _, it := range in {
			t := clip(clean(it.Text), MaxRunes)
			if t == "" || findExact(res, t) {
				continue
			}
			res = append(res, Item{Text: t, Quote: clip(clean(it.Quote), MaxQuote), Turn: it.Turn})
		}
		if len(res) > MaxItems {
			res = res[len(res)-MaxItems:]
		}
		return res
	}
	out.Clarified = cleanList(s.Clarified)
	out.Constraints = cleanList(s.Constraints)
	out.Open = cleanList(s.Open)
	for _, t := range s.Terms {
		term, meaning := clip(clean(t.Term), MaxRunes), clip(clean(t.Meaning), MaxRunes)
		if term == "" || meaning == "" || out.findTermExact(term) {
			continue
		}
		out.Terms = append(out.Terms, Term{Term: term, Meaning: meaning, Quote: clip(clean(t.Quote), MaxQuote), Turn: t.Turn})
	}
	if len(out.Terms) > MaxItems {
		out.Terms = out.Terms[len(out.Terms)-MaxItems:]
	}
	return out
}

func findExact(items []Item, text string) bool {
	for _, it := range items {
		if same(it.Text, text) {
			return true
		}
	}
	return false
}

func (s State) findTermExact(term string) bool {
	for _, t := range s.Terms {
		if same(t.Term, term) {
			return true
		}
	}
	return false
}

// Expand — термины задачи для поиска: если в запросе есть термин, к
// запросу добавляется его значение. Используется retrieve.RewriteCode.
//
// Термин ищется по основам слов (падеж не мешает: «барса», «барсом» —
// термин «барс»; «нашего зверя» — «наш зверь»), многословный — подряд
// идущими словами. Значение дописывается, только если его основ в запросе
// ещё нет. Второе значение — список раскрытого «барс → ирбис» для трассы
// поиска.
func (s State) Expand(query string) (string, []string) {
	if len(s.Terms) == 0 || strings.TrimSpace(query) == "" {
		return query, nil
	}
	qs := stems(query)
	var add, list []string
	for _, t := range s.Terms {
		ts := stems(t.Term)
		if len(ts) == 0 || !sequence(qs, ts) {
			continue
		}
		ms := stems(t.Meaning)
		if len(ms) == 0 || covered(ms, qs) == len(ms) {
			continue
		}
		add = append(add, t.Meaning)
		list = append(list, t.Term+" → "+t.Meaning)
		qs = append(qs, ms...)
	}
	if len(add) == 0 {
		return query, nil
	}
	return strings.TrimSpace(query) + " " + strings.Join(add, " "), list
}

// sequence — основы ts идут подряд в qs.
func sequence(qs, ts []string) bool {
	for i := 0; i+len(ts) <= len(qs); i++ {
		ok := true
		for j := range ts {
			if qs[i+j] != ts[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// endings — окончания русских слов, длинные первыми. Основа после отрезания
// — не короче трёх букв: «барс» и «барса» — одна основа, «барсук» — другая
// (окончания «ук» нет), «кот» не превращается в «ко».
var endings = []string{
	"иями", "ями", "ами", "ого", "его", "ому", "ему", "ыми", "ими", "иях", "ах", "ях", "ам", "ям", "ов", "ев", "ей",
	"ой", "ий", "ый", "ая", "яя", "ое", "ее", "ые", "ие", "ую", "юю", "ом", "ем", "ью", "а", "я", "ы", "и", "у", "ю", "е", "о", "ь",
}

// stems — основы слов строки (нижний регистр, ё → е).
func stems(s string) []string {
	fs := strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "ё", "е"), "Ё", "Е")), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fs))
	for _, w := range fs {
		out = append(out, stem(w))
	}
	return out
}

func stem(w string) string {
	n := utf8.RuneCountInString(w)
	for _, e := range endings {
		if strings.HasSuffix(w, e) && n-utf8.RuneCountInString(e) >= 3 {
			w = strings.TrimSuffix(w, e)
			break
		}
	}
	if strings.HasSuffix(w, "ь") && utf8.RuneCountInString(w) > 3 {
		w = strings.TrimSuffix(w, "ь")
	}
	return w
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func same(a, b string) bool {
	return strings.EqualFold(strings.ReplaceAll(clean(a), "ё", "е"), strings.ReplaceAll(clean(b), "ё", "е"))
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
