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
package task

import "errors"

// ErrNotImplemented — заглушка контракта.
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

// Clone — глубокая копия (ветвление, снимок до хода).
func (s State) Clone() State { return s }

// Block — текст блока для ведущего: «Задача разговора (ведёт код): цель …;
// уточнено …; ограничения …; термины …; открыто …». Пустое состояние —
// пустая строка (блок не уходит, механизм стоит ноль).
func (s State) Block() string { return "" }

// Patch — правка состояния от извлекателя.
type Patch struct {
	Goal *GoalPatch `json:"goal,omitempty"`
	// Add/Remove по спискам: clarified, constraints, terms, open.
	Add    []PatchItem `json:"add,omitempty"`
	Remove []PatchItem `json:"remove,omitempty"`
}

// GoalPatch — новая цель (или уточнение прежней) и цитата.
type GoalPatch struct {
	Text  string `json:"text"`
	Quote string `json:"quote"`
}

// PatchItem — пункт правки. List — clarified | constraints | terms | open.
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
}

// Apply применяет правку: цитата каждого пункта должна быть в реплике
// человека этого хода (user) — иначе пункт отклоняется с причиной; пределы
// MaxItems/MaxRunes; дубли не добавляются. Возвращает изменения.
func (s *State) Apply(p Patch, user string, turn int) []Change { return nil }

// Expand — термины задачи для поиска: если в запросе есть термин, к
// запросу добавляется его значение. Используется retrieve.RewriteCode.
func (s State) Expand(query string) (string, []string) { return query, nil }
