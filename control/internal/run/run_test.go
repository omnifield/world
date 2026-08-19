package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Настоящий запускатель проверяется на том, что есть на любой машине: `sh`. Докер и ssh
// сюда не зовутся намеренно — их в девбоксе нет, и тест, требующий контура, краснел бы не
// про код.

func TestExecОтдаётВыводИКодОтдельно(t *testing.T) {
	res, err := Exec{Timeout: 5 * time.Second}.Run(context.Background(), Command{
		Name: "sh", Args: []string{"-c", "printf машине; printf человеку >&2; exit 3"},
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Out != "машине" || res.Err != "человеку" || res.Code != 3 {
		t.Fatalf("вывод разъехался: out=%q err=%q code=%d", res.Out, res.Err, res.Code)
	}
}

func TestExecВезётStdin(t *testing.T) {
	res, err := Exec{Timeout: 5 * time.Second}.Run(context.Background(), Command{
		Name: "sh", Args: []string{"-c", "cat"}, Stdin: "личность",
	})
	if err != nil || res.Out != "личность" {
		t.Fatalf("stdin не доехал: out=%q err=%v", res.Out, err)
	}
}

// Различать «инструмента нет» и «инструмент сказал нет» — не придирка: отказы из них
// получаются разные, и чинят их разные люди.
func TestExecОтличаетОтсутствиеИнструмента(t *testing.T) {
	_, err := Exec{Timeout: 5 * time.Second}.Run(context.Background(), Command{Name: "нет-такого-инструмента-в-мире"})
	if !errors.Is(err, ErrNoTool) {
		t.Fatalf("ждали ErrNoTool, получили %v", err)
	}
}

func TestExecСтавитLCALL(t *testing.T) {
	res, _ := Exec{Timeout: 5 * time.Second}.Run(context.Background(), Command{
		Name: "sh", Args: []string{"-c", "echo $LC_ALL"},
	})
	if strings.TrimSpace(res.Out) != "C" {
		t.Fatalf("LC_ALL не выставлен: %q — на локализованной машине ступени отказа схлопнутся", res.Out)
	}
}

func TestExecНеЖдётДольшеПредела(t *testing.T) {
	started := time.Now()
	_, err := Exec{Timeout: 200 * time.Millisecond}.Run(context.Background(), Command{
		Name: "sh", Args: []string{"-c", "sleep 5"},
	})
	if err == nil {
		t.Fatal("предел времени не сработал — ручка HTTP висела бы столько же, сколько ssh")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("ждали дольше предела: %s", time.Since(started))
	}
}

// Инструмент, названный ПУТЁМ, отсутствует иначе, чем не найденный по PATH: приходит
// `fs.ErrNotExist`, а не `exec.ErrNotFound`. Разница видна только человеку — он получает
// «инструмент промолчал» вместо «инструмента нет» и идёт чинить связь вместо образа.
// Поймано пробой контроллера на пути к `deploy/remote.sh`.
func TestExecОтличаетОтсутствиеИнструментаПоПути(t *testing.T) {
	_, err := Exec{Timeout: 5 * time.Second}.Run(context.Background(), Command{Name: "/opt/нет-такого/remote.sh"})
	if !errors.Is(err, ErrNoTool) {
		t.Fatalf("ждали ErrNoTool, получили %v", err)
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ СТРОКА ДОЕЗЖАЕТ ДО ВЫЗЫВАЮЩЕГО, ПОКА ПРОЦЕСС ЖИВ. ЭТО И ЕСТЬ СВОЙСТВО.                │
// │                                                                                      │
// │ Сосед печатает метку пути ДО самого пути, потому что путь занимает минуты. Собери мы │
// │ вывод в буфер и разбери после `Wait` — метка доехала бы вместе с итогом, то есть      │
// │ перестала бы быть предупреждением (`WORLD2-150` B2).                                 │
// │                                                                                      │
// │ Проверяется не «позвали ли `OnLine`», а ИМЕННО «до»: процесс печатает строку и ЖДЁТ,  │
// │ пока тест не отпустит его файлом. Не доедь строка вовремя — ждать будет некому, и     │
// │ прогон упрётся в свой предел, а не зазеленеет.                                        │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestExecОтдаётСтрокуПокаПроцессЖив(t *testing.T) {
	ворота := filepath.Join(t.TempDir(), "открыть")

	пришло := make(chan string, 8)
	res, err := Exec{Timeout: 10 * time.Second}.Run(context.Background(), Command{
		Name: "sh",
		Args: []string{"-c", `printf 'REMOTE-PATH: image-copy\n'; n=0; while [ ! -f "$1" ] && [ $n -lt 200 ]; do sleep 0.05; n=$((n+1)); done; printf 'готово\n'`, "sh", ворота},
		OnLine: func(line string) {
			пришло <- line
			if strings.HasPrefix(line, "REMOTE-PATH: ") {
				// Отпускаем процесс ТОЛЬКО получив строку. Придёт она после выхода — открывать
				// ворота будет уже некому, и процесс досидит до своего предела.
				_ = os.WriteFile(ворота, []byte("иди"), 0o600)
			}
		},
	})
	if err != nil || res.Code != 0 {
		t.Fatalf("процесс не дождался строки — она доехала не вовремя: err=%v code=%d out=%q", err, res.Code, res.Out)
	}

	close(пришло)
	var строки []string
	for l := range пришло {
		строки = append(строки, l)
	}
	if len(строки) != 2 || строки[0] != "REMOTE-PATH: image-copy" || строки[1] != "готово" {
		t.Fatalf("строки пришли не так: %q", строки)
	}
	// Поток раздваивается, а не подменяется: `Out` по-прежнему целиком — по нему разбирается
	// код отказа соседа.
	if res.Out != "REMOTE-PATH: image-copy\nготово\n" {
		t.Fatalf("собранный вывод потерялся: %q", res.Out)
	}
}

// Половина строки — не строка. Метка, разрезанная на границе записи в трубу, доехала бы
// обрубком и означала бы другой путь либо никакой.
func TestExecНеОтдаётНезавершённыйХвост(t *testing.T) {
	var строки []string
	res, err := Exec{Timeout: 5 * time.Second}.Run(context.Background(), Command{
		Name: "sh", Args: []string{"-c", `printf 'целая\nхвост-без-перевода'`},
		OnLine: func(line string) { строки = append(строки, line) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(строки) != 1 || строки[0] != "целая" {
		t.Fatalf("незавершённый хвост уехал строкой: %q", строки)
	}
	if res.Out != "целая\nхвост-без-перевода" {
		t.Fatalf("хвост потерялся и из собранного вывода: %q", res.Out)
	}
}
