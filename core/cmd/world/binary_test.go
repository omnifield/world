package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omnifield/world/core/internal/schematest"
)

// Пробы этого файла гоняют НАСТОЯЩИЙ бинарь, а не обработчики в памяти.
// Причина ровно одна и она не про полноту: два главных вопроса задачи —
//
//	«без подкоманды остаётся дверь» и «сторож поднимается и в него входят» —
//
// это вопросы к ЗАПУСКУ процесса, и на обработчике в памяти они не задаются
// вовсе. Свои тесты показателем не являются (`kb:WORLD-32`), но и заменять
// живой прогон разговором с самим собой нельзя.

var (
	сборкаОдин    sync.Once
	каталогСборки string
	путьБинаря    string
	ошибкаСборки  error
)

// TestMain убирает за собой собранный бинарь. Сборка ленивая (её может не быть
// вовсе при -short), поэтому удаление здесь, а не в t.Cleanup: каталог общий на
// весь пакет, и снести его в конце одной пробы значит отобрать бинарь у
// остальных.
func TestMain(m *testing.M) {
	код := m.Run()
	if каталогСборки != "" {
		os.RemoveAll(каталогСборки)
	}
	os.Exit(код)
}

// бинарь собирает `world` один раз на весь пакет.
func бинарь(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("живой прогон бинаря пропущен: -short")
	}
	сборкаОдин.Do(func() {
		dir, err := os.MkdirTemp("", "world-bin")
		if err != nil {
			ошибкаСборки = err
			return
		}
		каталогСборки = dir
		путьБинаря = filepath.Join(dir, "world")
		out, err := exec.Command("go", "build", "-o", путьБинаря, ".").CombinedOutput()
		if err != nil {
			ошибкаСборки = err
			t.Logf("сборка: %s", out)
		}
	})
	if ошибкаСборки != nil {
		t.Fatalf("бинарь не собрался: %v", ошибкаСборки)
	}
	return путьБинаря
}

// свободныйАдрес — порт, который сейчас никем не занят.
func свободныйАдрес(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушатель не поднялся: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// служба поднимает бинарь фоном и снимает его в конце пробы.
func служба(t *testing.T, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command(бинарь(t), args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("служба %v не запустилась: %v", args, err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
}

// команда прогоняет разовую подкоманду и отдаёт код возврата и оба потока.
func команда(t *testing.T, env []string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(бинарь(t), args...)
	cmd.Env = append(os.Environ(), env...)
	out, errOut := &strings.Builder{}, &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, errOut
	err := cmd.Run()
	код := 0
	if e, ok := err.(*exec.ExitError); ok {
		код = e.ExitCode()
	} else if err != nil {
		t.Fatalf("команда %v не запустилась: %v", args, err)
	}
	return код, out.String(), errOut.String()
}

// дождаться ждёт, пока служба ответит: процесс поднимается не мгновенно, и
// стучаться в него сразу — значит проверять гонку, а не службу.
func дождаться(t *testing.T, url string) {
	t.Helper()
	срок := time.Now().Add(10 * time.Second)
	for time.Now().Before(срок) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("служба на %s не поднялась за 10 с", url)
}

func взять(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// Совместимость, ради которой подкоманды сделаны именно так: образ мира зовёт
// бинарь БЕЗ аргументов, и этот запуск обязан остаться дверью. Сломай развилку
// режимов — и уже собранный образ мира перестанет открывать поле.
func TestБезПодкомандыБинарьОстаётсяДверью(t *testing.T) {
	дверь := свободныйАдрес(t)
	служба(t, окружениеДвери(t, дверь))
	дождаться(t, "http://"+дверь+"/healthz")

	for _, путь := range []string{"/healthz", "/api/hello", "/api/locations"} {
		код, тело := взять(t, "http://"+дверь+путь)
		if код != http.StatusOK {
			t.Errorf("%s: получено %d (%s), ожидалось %d", путь, код, тело, http.StatusOK)
		}
	}
}

// Та же дверь, названная вслух: файл запуска вправе не полагаться на пустоту.
func TestЯвнаяПодкомандаDoorПоднимаетТуЖеДверь(t *testing.T) {
	дверь := свободныйАдрес(t)
	служба(t, окружениеДвери(t, дверь), "door")
	дождаться(t, "http://"+дверь+"/healthz")

	код, тело := взять(t, "http://"+дверь+"/api/locations")
	if код != http.StatusOK || !strings.Contains(тело, `"count"`) {
		t.Errorf("реестр за дверью: получено %d (%s)", код, тело)
	}
}

// ПРИЁМКА задачи целиком, живьём и одним бинарём: поднял сторожа → join →
// локация в поле, маршрут ведёт в неё → leave → её нет.
func TestЖивойПрогонСторожИВходВПоле(t *testing.T) {
	дверь := свободныйАдрес(t)
	служба(t, окружениеДвери(t, дверь))
	дождаться(t, "http://"+дверь+"/healthz")

	сторож := свободныйАдрес(t)
	служба(t, []string{
		"WORLD_ADDR=" + сторож,
		"WORLD_NAME=probe-loc",
		"WORLD_GIVES=проба входа в поле",
	}, "guard")
	дождаться(t, "http://"+сторож+"/healthz")

	окружение := []string{
		"WORLD_DOOR=" + дверь,
		"WORLD_NAME=probe-loc",
		"WORLD_GIVES=проба входа в поле",
		"WORLD_SELF_ADDR=" + сторож,
	}

	код, out, errOut := команда(t, окружение, "join")
	if код != 0 {
		t.Fatalf("join вернул %d: %s", код, errOut)
	}
	if !strings.Contains(out, "/probe-loc/") {
		t.Fatalf("join не вернул маршрут человеку: %s", out)
	}

	// Локация в поле — и маршрут за дверью ведёт именно в неё, а не в витрину.
	if _, тело := взять(t, "http://"+дверь+"/api/locations"); !strings.Contains(тело, "probe-loc") {
		t.Fatalf("локации нет в поле: %s", тело)
	}
	код2, тело := взять(t, "http://"+дверь+"/probe-loc/")
	if код2 != http.StatusOK || !strings.Contains(тело, `"mode":"guard"`) {
		t.Fatalf("маршрут ведёт не к сторожу: %d %s", код2, тело)
	}

	// who показывает то же самое поле.
	код, out, _ = команда(t, окружение, "who")
	if код != 0 || !strings.Contains(out, "probe-loc") {
		t.Fatalf("who вернул %d: %s", код, out)
	}

	// leave — и маршрута больше нет.
	if код, _, errOut := команда(t, окружение, "leave"); код != 0 {
		t.Fatalf("leave вернул %d: %s", код, errOut)
	}
	if _, тело := взять(t, "http://"+дверь+"/api/locations"); strings.Contains(тело, "probe-loc") {
		t.Fatalf("локация осталась в поле после leave: %s", тело)
	}
}

// Обязательный отказ ТЗ живьём: входим, НЕ подняв сторожа.
func TestЖивойОтказВходБезСторожа(t *testing.T) {
	дверь := свободныйАдрес(t)
	служба(t, окружениеДвери(t, дверь))
	дождаться(t, "http://"+дверь+"/healthz")

	код, _, errOut := команда(t, []string{
		"WORLD_DOOR=" + дверь,
		"WORLD_NAME=probe-loc",
		"WORLD_GIVES=проба",
		"WORLD_SELF_ADDR=" + свободныйАдрес(t), // сторожа по нему нет
	}, "join")

	if код != 1 {
		t.Fatalf("код возврата: получено %d, ожидалась 1 — вход не состоялся", код)
	}
	for _, кусок := range []string{"self-unreachable", "world guard"} {
		if !strings.Contains(errOut, кусок) {
			t.Errorf("в отказе нет %q: %s", кусок, errOut)
		}
	}
}

// Второй обязательный отказ живьём: двери не видно.
func TestЖивойОтказДвериНеВидно(t *testing.T) {
	код, _, errOut := команда(t, []string{
		"WORLD_DOOR=" + свободныйАдрес(t),
		"WORLD_NAME=probe-loc",
	}, "who")

	if код != 1 {
		t.Fatalf("код возврата: получено %d, ожидалась 1", код)
	}
	for _, кусок := range []string{"door-unreachable", "общей сети"} {
		if !strings.Contains(errOut, кусок) {
			t.Errorf("в отказе нет %q: %s", кусок, errOut)
		}
	}
}

// окружениеДвери — раскладка мира на время пробы: своё состояние поля, свои
// прогоны, витрины нет (её отсутствие дверь переживает, `tasker:WORLD-34`).
func окружениеДвери(t *testing.T, addr string) []string {
	t.Helper()
	dir := t.TempDir()
	return []string{
		"WORLD_ADDR=" + addr,
		"WORLD_DOOR_FILE=" + filepath.Join(dir, "field", "locations.json"),
		"WORLD_STAND_DIR=" + filepath.Join(dir, "stand"),
		"WORLD_WEB_DIR=" + filepath.Join(dir, "нет-витрины"),
	}
}

// ПРИЁМКА ЗАДАЧИ ЖИВЬЁМ, процессами и одним бинарём: поднял место → в поле оно
// пустое → НЕ ЗАХОДЯ ВНУТРЬ, через мир поставил на него постройку → место
// говорит, что на нём стоит → повтор назван повтором.
//
// Здесь это и проверяется по-настоящему: `docker exec` в прогоне нет, доступ
// идёт по маршруту двери, а клон появляется в каталоге места на диске — снаружи
// процесса, а не в его ответе (`kb:WORLD-56`, `kb:WORLD-32`).
func TestЖивойПрогонСтройкаЧерезМирБезЗаходаВнутрь(t *testing.T) {
	схема := schematest.Схема(t, map[string]string{"README.md": "постройка живого прогона"})

	дверь := свободныйАдрес(t)
	служба(t, окружениеДвери(t, дверь))
	дождаться(t, "http://"+дверь+"/healthz")

	стройка := filepath.Join(t.TempDir(), "стройка")
	сторож := свободныйАдрес(t)
	служба(t, []string{
		"WORLD_ADDR=" + сторож,
		"WORLD_NAME=probe-loc",
		"WORLD_GIVES=проба доступа внутрь места",
		"WORLD_BUILD_DIR=" + стройка,
	}, "guard")
	дождаться(t, "http://"+сторож+"/healthz")

	окружение := []string{
		"WORLD_DOOR=" + дверь,
		"WORLD_NAME=probe-loc",
		"WORLD_GIVES=проба доступа внутрь места",
		"WORLD_SELF_ADDR=" + сторож,
	}
	if код, _, errOut := команда(t, окружение, "join"); код != 0 {
		t.Fatalf("join вернул %d: %s", код, errOut)
	}

	// В поле место пустое — и это видно ЧЕРЕЗ ДВЕРЬ, по маршруту места.
	код, тело := взять(t, "http://"+дверь+"/probe-loc/api/build")
	if код != http.StatusOK || !strings.Contains(тело, `"built":false`) {
		t.Fatalf("свежее место не сказало, что оно пустое: %d %s", код, тело)
	}

	// Стройка. Внутрь контейнера никто не заходил: команда говорит с дверью.
	код2, out, errOut := команда(t, окружение, "build", "-schema", схема)
	if код2 != 0 {
		t.Fatalf("build вернул %d: %s", код2, errOut)
	}
	if !strings.Contains(out, "ВСТАЛА") || !strings.Contains(out, schematest.Коммит(t, схема)) {
		t.Fatalf("build не назвал исход и коммит: %s", out)
	}

	// Постройка на диске МЕСТА — это и есть «встала», а не строка в ответе.
	if _, err := os.Stat(filepath.Join(стройка, "build", "README.md")); err != nil {
		t.Fatalf("постройки нет в каталоге места: %v", err)
	}

	// Место говорит, что на нём стоит, — и через дверь, и командой.
	if код, тело := взять(t, "http://"+дверь+"/probe-loc/api/build"); код != http.StatusOK || !strings.Contains(тело, схема) {
		t.Fatalf("место не говорит о постройке: %d %s", код, тело)
	}
	if код, out, _ := команда(t, окружение, "what"); код != 0 || !strings.Contains(out, схема) {
		t.Fatalf("what вернул %d: %s", код, out)
	}

	// Повтор назван повтором, а не второй стройкой.
	код3, out, _ := команда(t, окружение, "build", "-schema", схема)
	if код3 != 0 || !strings.Contains(out, "уже стоит") {
		t.Fatalf("повтор: код %d, вывод %s", код3, out)
	}
}

// Обязательный отказ ТЗ живьём: тянемся к месту, которого в поле нет.
func TestЖивойОтказМестаНетВПоле(t *testing.T) {
	дверь := свободныйАдрес(t)
	служба(t, окружениеДвери(t, дверь))
	дождаться(t, "http://"+дверь+"/healthz")

	код, _, errOut := команда(t, []string{
		"WORLD_DOOR=" + дверь,
		"WORLD_NAME=такого-места-нет",
	}, "build", "-schema", "/схемы/постройка")

	if код != 1 {
		t.Fatalf("код возврата: получено %d, ожидалась 1", код)
	}
	for _, кусок := range []string{"location-unknown", "world who"} {
		if !strings.Contains(errOut, кусок) {
			t.Errorf("в отказе нет %q: %s", кусок, errOut)
		}
	}
}

// ПРИЁМКА ЗАДАЧИ ЖИВЬЁМ (`tasker:WORLD-103`), процессами и через дверь: на
// месте поднята застройка → тот же маршрут места отдаёт ЕЁ → застройка умерла →
// отказ называет причину и выход, а место при этом стоит.
//
// Живьём, а не обработчиком в памяти, потому что вопрос ровно в маршруте:
// «дверь → сторож → застройка» — это три процесса и две сети, и на одном
// обработчике такой вопрос не задаётся вовсе.
func TestЖивойПрогонМаршрутМестаВедётКЗастройке(t *testing.T) {
	дверь := свободныйАдрес(t)
	служба(t, окружениеДвери(t, дверь))
	дождаться(t, "http://"+дверь+"/healthz")

	// Застройка — то, что юзер поднял внутри места СВОИМИ руками: мир её не
	// разворачивал и разворачивать не умеет (канон WORLD2, узел 2.4).
	застройка := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "витрина застройки: "+r.URL.Path)
	}))
	t.Cleanup(застройка.Close)
	адресЗастройки := strings.TrimPrefix(застройка.URL, "http://")

	сторож := свободныйАдрес(t)
	служба(t, []string{
		"WORLD_ADDR=" + сторож,
		"WORLD_NAME=probe-loc",
		"WORLD_GIVES=проба маршрута места",
		"WORLD_BUILD_DIR=" + filepath.Join(t.TempDir(), "место"),
		"WORLD_BUILD_ADDR=" + адресЗастройки,
	}, "guard")
	дождаться(t, "http://"+сторож+"/healthz")

	окружение := []string{
		"WORLD_DOOR=" + дверь,
		"WORLD_NAME=probe-loc",
		"WORLD_GIVES=проба маршрута места",
		"WORLD_SELF_ADDR=" + сторож,
	}
	if код, _, errOut := команда(t, окружение, "join"); код != 0 {
		t.Fatalf("join вернул %d: %s", код, errOut)
	}

	// ГЛАВНОЕ: по маршруту места за дверью открывается ЗАСТРОЙКА.
	код, тело := взять(t, "http://"+дверь+"/probe-loc/витрина")
	if код != http.StatusOK || !strings.Contains(тело, "витрина застройки") {
		t.Fatalf("маршрут места ведёт не к застройке: %d %s", код, тело)
	}

	// Сторож остаётся хозяином порта: проба жизни за ним и через дверь.
	if код, тело := взять(t, "http://"+дверь+"/probe-loc/healthz"); код != http.StatusOK || !strings.Contains(тело, `"mode":"guard"`) {
		t.Fatalf("проба жизни места ушла в застройку: %d %s", код, тело)
	}

	// Застройка умерла — отказ называет причину и выход, а не отдаёт пустую
	// страницу и не молчит кодом 502.
	застройка.Close()
	код, тело = взять(t, "http://"+дверь+"/probe-loc/витрина")
	if код != http.StatusBadGateway {
		t.Fatalf("мёртвая застройка: получено %d (%s), ожидалось %d", код, тело, http.StatusBadGateway)
	}
	for _, кусок := range []string{"build-unreachable", "подними застройку", "WORLD_BUILD_ADDR"} {
		if !strings.Contains(тело, кусок) {
			t.Errorf("в отказе нет %q: %s", кусок, тело)
		}
	}
	// Место при этом СТОИТ и из поля не уходит.
	if код, тело := взять(t, "http://"+дверь+"/probe-loc/healthz"); код != http.StatusOK {
		t.Fatalf("место легло вместе с застройкой: %d %s", код, тело)
	}
}
