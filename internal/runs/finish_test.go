package runs_test

import (
	"testing"

	"github.com/AlexS8332/AnimalGuide_Task25/internal/rag"
	"github.com/AlexS8332/AnimalGuide_Task25/internal/runs"
)

// runs не может импортировать rag (цикл): имя завершающего инструмента —
// копия, и она обязана совпадать.
func TestAnswerToolIsRAGFinish(t *testing.T) {
	if runs.AnswerTool != rag.FinishName {
		t.Fatalf("runs.AnswerTool %q, rag.FinishName %q", runs.AnswerTool, rag.FinishName)
	}
}
