package run

import (
	"context"
	"strings"
	"sync"
)

// Fake — подменный запускатель для тестов и пробы. Лежит в обычном файле, а не в
// `_test.go`, намеренно: им пользуются тесты ДРУГИХ пакетов (ручки, ресурсы, скоуп), а
// из тестового файла чужой пакет ничего взять не может. Тот же приём, что у зоны `core`
// с пакетом `internal/schematest`.
//
// Fake НЕ притворяется докером и ssh. Он отвечает то, что ему сказали ответить, и
// ЗАПОМИНАЕТ вызовы: проверяем мы не «работает ли докер», а что контроллер позвал именно
// то, что обещал (`deploy/remote.sh add`, а не свой подъём), и как разобрал ответ.
type Fake struct {
	mu    sync.Mutex
	calls []Command

	// Answer — как отвечать. Nil означает «всё прошло, вывод пуст».
	Answer func(Command) (Result, error)
}

func (f *Fake) Run(_ context.Context, c Command) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.Answer == nil {
		return Result{}, nil
	}
	return f.Answer(c)
}

// Calls — что звали, по порядку.
func (f *Fake) Calls() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Command, len(f.calls))
	copy(out, f.calls)
	return out
}

// Line — вызов номер i одной строкой, для внятного текста провалившегося теста.
func (f *Fake) Line(i int) string {
	calls := f.Calls()
	if i < 0 || i >= len(calls) {
		return "(такого вызова не было)"
	}
	return calls[i].Name + " " + strings.Join(calls[i].Args, " ")
}

// Called — звали ли команду, у которой все перечисленные куски есть в строке вызова.
func (f *Fake) Called(parts ...string) bool {
	for i := range f.Calls() {
		line := f.Line(i)
		hit := true
		for _, p := range parts {
			if !strings.Contains(line, p) {
				hit = false
				break
			}
		}
		if hit {
			return true
		}
	}
	return false
}

var _ Runner = (*Fake)(nil)
