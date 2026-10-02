package features

import "testing"

func TestPresetRAG(t *testing.T) {
	r := Catalog()
	s, err := r.ParsePreset(PresetRAG, "", r.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []Name{RAG, RAGFilter, RAGRewrite, RAGCite, Task, Extract, Window, Compact, Charter, Guard} {
		if !s.On(n) {
			t.Errorf("пресет rag: %s выключен", n)
		}
	}
	if err := r.Validate(s); err != nil {
		t.Fatal(err)
	}
	// Пресет — поверх базового набора: выключенное там остаётся выключенным.
	if s, _ := r.ParsePreset(PresetRAG, "", r.Defaults().With(Guard, false)); s.On(Guard) || !s.On(RAGCite) {
		t.Fatalf("пресет поверх базы: %s", s)
	}
	// -features поверх пресета.
	s, err = r.ParsePreset(PresetRAG, "-guard", r.Defaults())
	if err != nil || s.On(Guard) || !s.On(RAGCite) {
		t.Fatalf("пресет с правкой: %v %s", err, s)
	}
	// Пустой пресет — обычный разбор.
	s, err = r.ParsePreset("", "+rag", r.Defaults())
	if err != nil || !s.On(RAG) || s.On(RAGCite) {
		t.Fatalf("без пресета: %v %s", err, s)
	}
	if _, err := r.ParsePreset("nope", "", r.Defaults()); err == nil {
		t.Fatal("неизвестный пресет")
	}
	if len(PresetNames()) == 0 || PresetNames()[0] != PresetRAG {
		t.Fatal(PresetNames())
	}
}
