package run

import (
	"context"
	"errors"
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
