package features

import (
	"fmt"
	"sort"
	"strings"
)

// PresetRAG — пресет «Справочная (RAG)» (v25): база знаний с фильтром,
// переписыванием запроса и ответом с источниками, плюс память задачи —
// длинный разговор по базе, где цель и договорённости не теряются, а
// каждый ответ несёт источники.
const PresetRAG = "rag"

// presets — именованные наборы механизмов строкой флага -features. Пресет
// ложится поверх базового набора (умолчания приложения или сервера): он
// добавляет механизмы, а не выключает базовые (память, профиль, окно,
// сокращение, извлекатель — без него память задачи не пишется).
var presets = map[string]struct{ Title, Spec string }{
	PresetRAG: {Title: "Справочная (RAG)", Spec: "+rag,+rag.filter,+rag.rewrite,+rag.cite,+task"},
}

// PresetSpec — строка флага -features для пресета; пусто — ошибки нет и
// добавлять нечего.
func PresetSpec(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	p, ok := presets[name]
	if !ok {
		return "", fmt.Errorf("неизвестный пресет %q; известны: %s", name, strings.Join(PresetNames(), ", "))
	}
	return p.Spec, nil
}

// PresetNames — имена пресетов по алфавиту.
func PresetNames() []string {
	out := make([]string, 0, len(presets))
	for n := range presets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ParsePreset — набор механизмов по пресету и строке -features поверх него:
// «-preset rag -features -guard» — справочная без стража. Пустой пресет —
// то же, что Parse(spec, base).
func (r *Registry) ParsePreset(preset, spec string, base Set) (Set, error) {
	ps, err := PresetSpec(preset)
	if err != nil {
		return Set{}, err
	}
	if ps == "" {
		return r.Parse(spec, base)
	}
	return r.Parse(ps+","+spec, base)
}
