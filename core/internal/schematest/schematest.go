// Package schematest — СХЕМА ДЛЯ ПРОБ: настоящий репозиторий во временном
// каталоге.
//
// Зачем настоящий, а не заглушка. Постройка приезжает СХЕМОЙ — репозиторием
// (`kb:WORLD-27`), и «постройка встала» означает ровно одно: клон схемы лежит
// на диске места, а его коммит измерен. Подменив git заглушкой, мы проверяли бы
// собственную выдумку о том, как он себя ведёт, — а пробы обязаны краснеть на
// том, что случается на самом деле (`kb:WORLD-32`).
//
// Пакет живёт отдельно, потому что нужен трём пакетам проб сразу (`build`,
// `field`, `cmd/world`), а три копии одного помощника разъезжаются молча.
package schematest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Требуется пропускает пробу, если git в этой среде нет.
//
// Пропуск, а не провал: git внутри места — зона `deploy` (`tasker:WORLD-95`), и
// его отсутствие ловится своей пробой (`git-missing`), а не падением всех
// остальных. Пропуск при этом ВИДЕН в выводе — молча зелёным он не станет.
func Требуется(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git в этой среде не найден (%v): пробы стройки пропущены — схеме нечем ехать", err)
	}
}

// Схема — репозиторий с одним коммитом и заданным содержимым. Отдаёт путь к
// нему: путь от корня — законный адрес схемы, и в пробах он честнее сети.
func Схема(t *testing.T, файлы map[string]string) string {
	t.Helper()
	Требуется(t)

	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	for имя, содержимое := range файлы {
		путь := filepath.Join(dir, имя)
		if err := os.MkdirAll(filepath.Dir(путь), 0o755); err != nil {
			t.Fatalf("каталог схемы не создан: %v", err)
		}
		if err := os.WriteFile(путь, []byte(содержимое), 0o644); err != nil {
			t.Fatalf("файл схемы не записан: %v", err)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "схема пробы")
	return dir
}

// Пустая — репозиторий БЕЗ единого коммита. Случай честный: у такой схемы HEAD
// не существует, и коммит измерить нечем — пробе надо убедиться, что мир об
// этом говорит, а не проставляет правдоподобное (`kb:WORLD-32`).
func Пустая(t *testing.T) string {
	t.Helper()
	Требуется(t)

	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	return dir
}

// Коммит — что стоит в HEAD схемы. Нужен пробам, сверяющим паспорт постройки с
// тем, что на самом деле приехало.
func Коммит(t *testing.T, репозиторий string) string {
	t.Helper()
	return strings.TrimSpace(git(t, репозиторий, "rev-parse", "HEAD"))
}

// git гоняет команду в СВОЁМ окружении: чужой глобальный конфиг сюда не
// пускается (`HOME` подменён, системный конфиг выключен), иначе проба зависела
// бы от настроек машины, на которой её запустили.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=проба",
		"GIT_AUTHOR_EMAIL=probe@example.invalid",
		"GIT_COMMITTER_NAME=проба",
		"GIT_COMMITTER_EMAIL=probe@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s в %s: %v (%s)", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}
