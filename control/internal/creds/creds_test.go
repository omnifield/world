package creds

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/omnifield/world/control/internal/creds/sshtest"
)

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЧТО СТЕРЕЖЁТСЯ ЗДЕСЬ (`WORLD2-141`): пароль — способ получить ключ, а не транспорт.  │
// │                                                                                      │
// │ Проверяется это на НАСТОЯЩЕМ ssh-обмене с подставной машиной, а не на пересказе:      │
// │ команда уходит туда по-настоящему и выполняется её шеллом в её домашнем каталоге.    │
// │ Смотрим потом В ФАЙЛ той машины — ответ контроллера мог бы сказать что угодно.        │
// └─────────────────────────────────────────────────────────────────────────────────────┘

func машина(t *testing.T) *sshtest.Machine {
	t.Helper()
	m, err := sshtest.Start("world", "секрет", filepath.Join(t.TempDir(), "дом"))
	if err != nil {
		t.Fatalf("подставная машина не поднялась: %v", err)
	}
	t.Cleanup(m.Close)
	return m
}

func куда(t *testing.T, m *sshtest.Machine) Machine {
	t.Helper()
	host, port, ok := strings.Cut(m.Addr, ":")
	if !ok {
		t.Fatalf("адрес машины не разобрать: %s", m.Addr)
	}
	п := 0
	for _, c := range port {
		п = п*10 + int(c-'0')
	}
	return Machine{User: m.User, Host: host, Port: п}
}

func TestПарольМеняетсяНаКлюч(t *testing.T) {
	m := машина(t)
	pair, ref := Generate()
	if ref != nil {
		t.Fatal(ref.Why)
	}
	known := filepath.Join(t.TempDir(), "known_hosts")

	if ref := Install(context.Background(), куда(t, m), m.Pass, pair.Authorized, known, 5); ref != nil {
		t.Fatalf("ключ не лёг на машину: %s — %s", ref.Code, ref.Why)
	}

	// Смотрим В ФАЙЛ машины: ровно одна строка, и это наш ключ с подписью.
	файл := strings.TrimSpace(m.Authorized())
	if файл != strings.TrimSpace(pair.Authorized) {
		t.Fatalf("в authorized_keys машины лежит не то:\n%s", файл)
	}
	if !strings.Contains(файл, comment) {
		t.Fatalf("строка не подписана — человек не поймёт, откуда она: %s", файл)
	}

	// Ключ машины записан при первой встрече: дальше его сверяет системный ssh.
	if data, err := os.ReadFile(known); err != nil || len(strings.TrimSpace(string(data))) == 0 {
		t.Fatalf("ключ машины не записан в known_hosts: %v", err)
	}
}

// ПОВТОРНЫЙ ЗАХОД НЕ ПЛОДИТ СТРОК. Иначе в чужом файле однажды оказался бы десяток наших
// ключей, и убирать их пришлось бы человеку руками.
func TestПовторныйЗаходНеПлодитСтрок(t *testing.T) {
	m := машина(t)
	pair, _ := Generate()
	known := filepath.Join(t.TempDir(), "known_hosts")

	for i := 0; i < 3; i++ {
		if ref := Install(context.Background(), куда(t, m), m.Pass, pair.Authorized, known, 5); ref != nil {
			t.Fatalf("заход %d: %s — %s", i+1, ref.Code, ref.Why)
		}
	}
	if n := strings.Count(strings.TrimSpace(m.Authorized()), "\n"); n != 0 {
		t.Fatalf("в authorized_keys машины больше одной строки:\n%s", m.Authorized())
	}
}

// «НЕ ЗАПИСАЛОСЬ» И «ЗАПИСАЛОСЬ» РАЗЛИЧАЮТСЯ ИЗМЕРЕНИЕМ, а не кодом возврата: контроллер
// считает свои строки в файле и требует ровно одну (`WORLD2` 4.2 п. 5).
func TestНеЗаписалосьЭтоОтказАНеУспех(t *testing.T) {
	дом := filepath.Join(t.TempDir(), "дом")
	m, err := sshtest.Start("world", "секрет", дом)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// Дом только на чтение: `~/.ssh` не завести, файл не создать.
	if err := os.Chmod(дом, 0o500); err != nil {
		t.Skipf("права на каталог не меняются: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(дом, 0o700) })

	pair, _ := Generate()
	ref := Install(context.Background(), куда(t, m), m.Pass, pair.Authorized, "", 5)
	if ref == nil {
		t.Fatal("контроллер отчитался успехом, ничего не записав")
	}
	if ref.Code != "key-not-installed" || len(ref.Ways) == 0 {
		t.Fatalf("отказ не тот: %+v", ref)
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ УСПЕХ РЕШАЕТСЯ СЧЁТОМ, А НЕ КОДОМ ВОЗВРАТА. Машина вправе промолчать: команда прошла,│
// │ а измерить нечего. Отчитаться успехом в этом случае значит соврать про чужой файл.   │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestУспехРешаетсяСчётомАНеКодомВозврата(t *testing.T) {
	m := машина(t)
	m.Silent = true
	pair, _ := Generate()

	ref := Install(context.Background(), куда(t, m), m.Pass, pair.Authorized, "", 5)
	if ref == nil {
		t.Fatal("машина промолчала, а контроллер отчитался успехом — это ложь про чужой файл")
	}
	if ref.Code != "key-not-installed" || len(ref.Ways) == 0 {
		t.Fatalf("отказ не тот: %+v", ref)
	}
}

// Пароль не подошёл — это отказ ДОСТУПА, и он не смешивается с «машины не видно»: чинят
// их разные вещи (`WORLD2` 2.3).
func TestНеверныйПарольЭтоОтказДоступа(t *testing.T) {
	m := машина(t)
	pair, _ := Generate()

	ref := Install(context.Background(), куда(t, m), "не-тот", pair.Authorized, "", 5)
	if ref == nil || ref.Code != "access-denied" {
		t.Fatalf("отказ не тот: %+v", ref)
	}
	if len(ref.Ways) == 0 {
		t.Fatal("отказ без выхода — тупик")
	}
	if m.Authorized() != "" {
		t.Fatal("на машине что-то появилось при неверном пароле")
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПАРОЛЬ НЕ УТЕКАЕТ НИКУДА. Здесь — что его нет в ОТКАЗЕ: отказ читают люди, и он же   │
// │ уезжает в журнал. Что его нет в скоупе, в связке и в журнале ручек, стерегут тесты   │
// │ ручек и проба зоны.                                                                  │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestПароляНетВОтказе(t *testing.T) {
	m := машина(t)
	pair, _ := Generate()
	пароль := "очень-секретный-пароль-1234"

	ref := Install(context.Background(), куда(t, m), пароль, pair.Authorized, "", 5)
	if ref == nil {
		t.Fatal("подставная машина приняла чужой пароль")
	}
	весь := ref.Code + " " + ref.Why + " " + strings.Join(ref.Ways, " ")
	if strings.Contains(весь, пароль) {
		t.Fatalf("пароль утёк в отказ: %s", весь)
	}
}

// ВИД КРЕД НАЗЫВАЕТСЯ ЯВНО. Пустое и неизвестное — разные отказы: первое значит «не
// сказали», второе «сказали не то».
func TestВидКредНазываетсяЯвно(t *testing.T) {
	if k, ref := ParseKind("key"); ref != nil || k != Key {
		t.Fatalf("ключ не принят: %v %+v", k, ref)
	}
	if k, ref := ParseKind("пароль"); ref != nil || k != Password {
		t.Fatalf("пароль по-русски не принят: %v %+v", k, ref)
	}
	if _, ref := ParseKind(""); ref == nil || ref.Code != "no-creds-kind" {
		t.Fatalf("вид не назван, а его приняли: %+v", ref)
	}
	if _, ref := ParseKind("что-то"); ref == nil || ref.Code != "bad-creds-kind" {
		t.Fatalf("неизвестный вид принят: %+v", ref)
	}
}

// Ключ юзера лежит в скоупе приватным; публичный производен от него и в формате не
// хранится — второе поле того же самого однажды разъехалось бы с первым.
func TestПубличныйКлючПроизводенОтПриватного(t *testing.T) {
	pair, _ := Generate()
	line, ref := Authorized(pair.Private)
	if ref != nil {
		t.Fatal(ref.Why)
	}
	if line != pair.Authorized {
		t.Fatalf("публичный ключ собрался иначе:\n%s\n%s", line, pair.Authorized)
	}
	if _, ref := Authorized("это не ключ"); ref == nil || ref.Code != "bad-key" {
		t.Fatalf("мусор вместо ключа принят: %+v", ref)
	}
}

// До машины не дозвонились — это НЕ «пароль не подошёл»: ступени связи разные, и чинят их
// разные люди.
func TestДоМашиныНеДозвонилисьЭтоДругаяСтупень(t *testing.T) {
	m := машина(t)
	куда := куда(t, m)
	m.Close() // порт освободился — теперь по адресу никого

	ref := Install(context.Background(), куда, "секрет", "ключ", "", 2)
	if ref == nil || ref.Code != "no-answer" {
		t.Fatalf("отказ не тот: %+v", ref)
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПОДМЕНА МАШИНЫ ОТВЕРГАЕТСЯ, А НЕ ЗАПИСЫВАЕТСЯ ВТОРОЙ СТРОКОЙ.                        │
// │                                                                                      │
// │ Живой прогон 2026-08-20: обе VPS переустановили, у машины сменился ключ хоста, а том │
// │ со связкой это пережил. Наш слой заходил паролем МОЛЧА, а системный `ssh`, которым    │
// │ идёт подъём, тот же ключ отвергал. Юзер получал «креды не приняты» — и шёл чинить     │
// │ пароль, который никто не отвергал.                                                    │
// │                                                                                      │
// │ Здесь стережётся ОБА следствия сразу: и что мы не отдадим ключ юзера чужой машине,    │
// │ и что причина названа своим именем.                                                   │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestПодменаМашиныОтвергаетсяСвоимКодом(t *testing.T) {
	m := машина(t)
	pair, _ := Generate()
	known := filepath.Join(t.TempDir(), "known_hosts")

	// Записываем для ЭТОГО адреса ключ другой машины — так выглядит переустановленный
	// (или подменённый) хост с точки зрения связки.
	чужой, _ := Generate()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(чужой.Authorized))
	if err != nil {
		t.Fatalf("чужой ключ не разобрался: %v", err)
	}
	строка := knownhosts.Line([]string{knownhosts.Normalize(m.Addr)}, pub) + "\n"
	if err := os.WriteFile(known, []byte(строка), 0o600); err != nil {
		t.Fatal(err)
	}

	ref := Install(context.Background(), куда(t, m), m.Pass, pair.Authorized, known, 5)
	if ref == nil {
		t.Fatal("подменённая машина принята молча — ключ юзера уехал бы не туда")
	}
	if ref.Code != "host-key-changed" {
		t.Fatalf("причина названа не своим именем: %s — %s", ref.Code, ref.Why)
	}
	// Ключ юзера на такую машину не кладётся ВОВСЕ: до команды дело не доходит.
	if strings.TrimSpace(m.Authorized()) != "" {
		t.Fatalf("ключ юзера лёг на подменённую машину:\n%s", m.Authorized())
	}
}

// А НЕЗНАКОМАЯ машина принимается — иначе первый заход был бы невозможен вовсе.
func TestНезнакомаяМашинаЗапоминается(t *testing.T) {
	m := машина(t)
	pair, _ := Generate()
	known := filepath.Join(t.TempDir(), "known_hosts")

	if ref := Install(context.Background(), куда(t, m), m.Pass, pair.Authorized, known, 5); ref != nil {
		t.Fatalf("первый заход отказал: %s — %s", ref.Code, ref.Why)
	}
	data, err := os.ReadFile(known)
	if err != nil || !strings.Contains(string(data), knownhosts.Normalize(m.Addr)) {
		t.Fatalf("ключ незнакомой машины не запомнился: %v %s", err, data)
	}
}
