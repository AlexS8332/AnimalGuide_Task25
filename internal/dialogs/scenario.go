// Package dialogs — длинные сценарии разговора со справочной по базе (v25):
// файл сценария (реплики человека, ожидаемые источники, ограничения, цель)
// и проверки кодом по итогам каждого хода — есть ли источники, совпали ли
// ожидаемые doc_id, соблюдены ли ограничения, названа ли цель на
// контрольных репликах. Судьи здесь нет: проверки — признаки текста, и
// отчёт поэтому всегда показывает сами ответы рядом с отметками.
//
// Прогон — Play поверх Asker: kb chat -script ходит в запущенное
// приложение по REST, стенд может ходить в менеджер ходов напрямую.
package dialogs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Пределы сценария: задание 25 — «2 длинных сценария по 10–15 сообщений».
const (
	MinTurns = 10
	MaxTurns = 15
)

// Метки реплик. Проверку включает только MarkGoal; остальные — что
// реплика испытывает (для отчёта и разбора провалов).
const (
	MarkGoal       = "goal"       // контрольная: «напомни цель» — ответ называет цель
	MarkGoalSet    = "goal_set"   // цель сказана в этой реплике
	MarkConstraint = "constraint" // ограничение задано или сменено
	MarkTerm       = "term"       // термин диалога («барс» = ирбис)
	MarkPronoun    = "pronoun"    // продолжение с местоимением или без подлежащего
	MarkOfftopic   = "offtopic"   // уход в сторону от цели
	MarkReturn     = "return"     // возврат к цели
	MarkBare       = "bare"       // голое название животного
	MarkCompare    = "compare"    // сравнение двух видов
	MarkUnknown    = "unknown"    // вопрос вне базы
)

var marks = []string{MarkGoal, MarkGoalSet, MarkConstraint, MarkTerm, MarkPronoun, MarkOfftopic, MarkReturn, MarkBare, MarkCompare, MarkUnknown}

// Виды ограничений ответа.
const (
	RuleNoLatin      = "no_latin"      // в ответе нет латинского бинома
	RuleLatin        = "latin"         // в ответе есть латинский бином
	RuleMaxSentences = "max_sentences" // не больше N предложений
	RuleIUCN         = "iucn"          // в ответе назван статус МСОП
)

var rules = []string{RuleNoLatin, RuleLatin, RuleMaxSentences, RuleIUCN}

// Scenario — длинный разговор: реплики человека по порядку.
type Scenario struct {
	Schema int    `json:"schema"`
	ID     string `json:"id"`
	Title  string `json:"title"`
	// Preset — набор механизмов диалога (features.PresetRAG — «rag»).
	Preset string `json:"preset,omitempty"`
	Goal   Goal   `json:"goal"`
	Rules  []Rule `json:"rules,omitempty"`
	Turns  []Turn `json:"turns"`
	Note   string `json:"note,omitempty"`
}

// Goal — цель разговора и признаки того, что ответ её назвал: каждая
// группа ключевых слов («доклад|сообщени») должна найтись в ответе
// контрольной реплики (без учёта регистра и ё, по началу слова).
type Goal struct {
	Text     string   `json:"text"`
	Keywords []string `json:"keywords"`
}

// Rule — ограничение ответа на ходах From..To (с 1; To 0 — до конца) или
// только на ходах Turns. «Простыми словами» кодом не проверить — такие
// ограничения живут в тексте реплики и в Note для судьи.
type Rule struct {
	Kind  string `json:"kind"`
	N     int    `json:"n,omitempty"`
	From  int    `json:"from,omitempty"`
	To    int    `json:"to,omitempty"`
	Turns []int  `json:"turns,omitempty"`
	Note  string `json:"note,omitempty"`
}

// Applies — действует ли ограничение на ходе n (с 1).
func (r Rule) Applies(n int) bool {
	if len(r.Turns) > 0 {
		return slices.Contains(r.Turns, n)
	}
	return n >= r.From && (r.To == 0 || n <= r.To)
}

// Turn — реплика человека и чего от ответа ждать. Docs — doc_id, каждый
// должен быть среди источников ответа; альтернативы — через «|»
// («red-panda|mdd-carnivora»). Unknown — вопрос вне базы: ждём «не знаю».
// Реплика с меткой goal — контрольная: источники — память задачи.
type Turn struct {
	Text    string   `json:"text"`
	Docs    []string `json:"docs,omitempty"`
	Unknown bool     `json:"unknown,omitempty"`
	Marks   []string `json:"marks,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// Has — есть ли у реплики метка.
func (t Turn) Has(mark string) bool { return slices.Contains(t.Marks, mark) }

// Load читает сценарий; неизвестное поле — ошибка (опечатка в «unknonw»
// иначе молча дала бы не тот сценарий).
func Load(path string) (Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	return Parse(data)
}

// Parse разбирает сценарий.
func Parse(data []byte) (Scenario, error) {
	var s Scenario
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Scenario{}, fmt.Errorf("сценарий не разобрался: %w", err)
	}
	return s, nil
}

// Validate — сценарий годен к прогону: 10–15 реплик, у каждой текст, у
// вопроса по базе — ожидаемые doc_id (известные, если docs задан), у
// вопроса вне базы — их нет; метки и ограничения известны; контрольная
// реплика есть и цель с ключевыми словами задана. docs == nil — doc_id не
// сверять с корпусом.
func Validate(s Scenario, docs map[string]bool) error {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if s.Schema != 1 {
		bad("schema %d, ждали 1", s.Schema)
	}
	if strings.TrimSpace(s.ID) == "" {
		bad("нет id")
	}
	if n := len(s.Turns); n < MinTurns || n > MaxTurns {
		bad("реплик %d, ждали %d–%d", n, MinTurns, MaxTurns)
	}
	if strings.TrimSpace(s.Goal.Text) == "" || len(s.Goal.Keywords) == 0 {
		bad("нет цели или её ключевых слов")
	}
	controls := 0
	for i, t := range s.Turns {
		n := i + 1
		if strings.TrimSpace(t.Text) == "" {
			bad("реплика %d пустая", n)
		}
		for _, m := range t.Marks {
			if !slices.Contains(marks, m) {
				bad("реплика %d: неизвестная метка %q", n, m)
			}
		}
		switch {
		case t.Has(MarkGoal):
			controls++
			if len(t.Docs) > 0 || t.Unknown {
				bad("реплика %d: контрольная реплика отвечает по памяти задачи — без docs и unknown", n)
			}
		case t.Unknown:
			if len(t.Docs) > 0 {
				bad("реплика %d: вопрос вне базы с ожидаемыми docs", n)
			}
		case len(t.Docs) == 0:
			bad("реплика %d: нет ожидаемых docs (вопрос вне базы — unknown: true)", n)
		}
		for _, d := range t.Docs {
			for _, alt := range strings.Split(d, "|") {
				if alt = strings.TrimSpace(alt); alt == "" || (docs != nil && !docs[alt]) {
					bad("реплика %d: doc_id %q нет в корпусе", n, alt)
				}
			}
		}
	}
	if controls == 0 {
		bad("нет контрольной реплики (метка goal)")
	}
	for i, r := range s.Rules {
		if !slices.Contains(rules, r.Kind) {
			bad("ограничение %d: неизвестный вид %q", i+1, r.Kind)
		}
		if r.Kind == RuleMaxSentences && r.N <= 0 {
			bad("ограничение %d: max_sentences без n", i+1)
		}
		if len(r.Turns) == 0 && (r.From < 1 || (r.To != 0 && r.To < r.From)) {
			bad("ограничение %d: ходы from %d, to %d", i+1, r.From, r.To)
		}
		for _, n := range r.Turns {
			if n < 1 || n > len(s.Turns) {
				bad("ограничение %d: нет хода %d", i+1, n)
			}
		}
	}
	if len(problems) > 0 {
		return errors.New("сценарий " + s.ID + ": " + strings.Join(problems, "; "))
	}
	return nil
}

// CorpusDocs — doc_id документов корпуса (<dir>/*.json с полем doc_id).
func CorpusDocs(dir string) (map[string]bool, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var d struct {
			DocID string `json:"doc_id"`
		}
		if json.Unmarshal(data, &d) == nil && d.DocID != "" {
			out[d.DocID] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("в %s нет документов корпуса", dir)
	}
	return out, nil
}
