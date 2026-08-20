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
	pair, ref := Generate(Sign("http://проба:8070/"))
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
	pair, _ := Generate(Sign("http://проба:8070/"))
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

	pair, _ := Generate(Sign("http://проба:8070/"))
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
	pair, _ := Generate(Sign("http://проба:8070/"))

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
	pair, _ := Generate(Sign("http://проба:8070/"))

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
	pair, _ := Generate(Sign("http://проба:8070/"))
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
	pair, _ := Generate(Sign("http://проба:8070/"))
	line, ref := Authorized(pair.Private, Sign("http://проба:8070/"))
	if ref != nil {
		t.Fatal(ref.Why)
	}
	if line != pair.Authorized {
		t.Fatalf("публичный ключ собрался иначе:\n%s\n%s", line, pair.Authorized)
	}
	if _, ref := Authorized("это не ключ", comment); ref == nil || ref.Code != "bad-key" {
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
	pair, _ := Generate(Sign("http://проба:8070/"))
	known := filepath.Join(t.TempDir(), "known_hosts")

	// Записываем для ЭТОГО адреса ключ другой машины — так выглядит переустановленный
	// (или подменённый) хост с точки зрения связки.
	чужой, _ := Generate(Sign("http://проба:8070/"))
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
	pair, _ := Generate(Sign("http://проба:8070/"))
	known := filepath.Join(t.TempDir(), "known_hosts")

	if ref := Install(context.Background(), куда(t, m), m.Pass, pair.Authorized, known, 5); ref != nil {
		t.Fatalf("первый заход отказал: %s — %s", ref.Code, ref.Why)
	}
	data, err := os.ReadFile(known)
	if err != nil || !strings.Contains(string(data), knownhosts.Normalize(m.Addr)) {
		t.Fatalf("ключ незнакомой машины не запомнился: %v %s", err, data)
	}
}

// СНЯЛИ УЧАСТОК — УХОДИМ С МАШИНЫ СОВСЕМ. Пароля к этому времени нет и быть не должно, а
// ключ есть: им и заходим, чтобы убрать СВОЮ строку. Пока строки копились, обещание
// «убрать доступ можно, удалив эту строку» было неисполнимым — они подписаны одинаково, и
// какая чья, юзер не знает (живой прогон 2026-08-20: восемь строк на одной машине).
func TestСнятиеУбираетСвоюСтрокуЗаходомПоКлючу(t *testing.T) {
	m := машина(t)
	pair, _ := Generate(Sign("http://проба:8070/"))
	known := filepath.Join(t.TempDir(), "known_hosts")

	if ref := Install(context.Background(), куда(t, m), m.Pass, pair.Authorized, known, 5); ref != nil {
		t.Fatalf("ключ не лёг: %s", ref.Why)
	}
	if strings.TrimSpace(m.Authorized()) == "" {
		t.Fatal("ключа на машине нет — проверять нечего")
	}

	RemoveByKey(context.Background(), куда(t, m), pair.Private, pair.Authorized, known, 5)

	if лежит := strings.TrimSpace(m.Authorized()); лежит != "" {
		t.Fatalf("после снятия участка наша строка осталась на машине:\n%s", лежит)
	}
}

// ЧУЖИЕ СТРОКИ НЕ ТРОГАЕМ: файл принадлежит юзеру, и всё, что в нём не наше, — не наше дело.
func TestСнятиеНеТрогаетЧужиеСтроки(t *testing.T) {
	m := машина(t)
	наша, _ := Generate(Sign("http://проба:8070/"))
	чужая, _ := Generate(Sign("http://проба:8070/"))
	known := filepath.Join(t.TempDir(), "known_hosts")

	if ref := Install(context.Background(), куда(t, m), m.Pass, чужая.Authorized, known, 5); ref != nil {
		t.Fatalf("чужой ключ не лёг: %s", ref.Why)
	}
	if ref := Install(context.Background(), куда(t, m), m.Pass, наша.Authorized, known, 5); ref != nil {
		t.Fatalf("наш ключ не лёг: %s", ref.Why)
	}

	RemoveByKey(context.Background(), куда(t, m), наша.Private, наша.Authorized, known, 5)

	лежит := strings.TrimSpace(m.Authorized())
	if strings.Contains(лежит, strings.TrimSpace(наша.Authorized)) {
		t.Fatalf("своя строка не убрана:\n%s", лежит)
	}
	if !strings.Contains(лежит, strings.TrimSpace(чужая.Authorized)) {
		t.Fatalf("убрана ЧУЖАЯ строка — мир распорядился не своим:\n%s", лежит)
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПОДПИСЬ — СЛОВА ДЛЯ ЧЕЛОВЕКА, А ДОСТУП ДАЁТ КЛЮЧ. Поэтому строки сравниваются ПО     │
// │ КЛЮЧУ: смени мы подпись и ищи по ней — на машине оказалась бы ВТОРАЯ строка с тем же │
// │ ключом, и убрать старую было бы нечем.                                                │
// │                                                                                      │
// │ Так и накопилось восемь строк на одной машине (живой прогон 2026-08-20).             │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestСменаПодписиНеПлодитСтрокИСтараяУбирается(t *testing.T) {
	m := машина(t)
	pair, _ := Generate(Sign("http://первый:8070/"))
	known := filepath.Join(t.TempDir(), "known_hosts")

	// Ключ лёг со СТАРОЙ подписью.
	if ref := Install(context.Background(), куда(t, m), m.Pass, pair.Authorized, known, 5); ref != nil {
		t.Fatalf("первый заход: %s", ref.Why)
	}

	// Тот же ключ, НОВАЯ подпись — строк не прибавляется.
	новая, ref := Authorized(pair.Private, Sign("http://второй:8070/"))
	if ref != nil {
		t.Fatal(ref.Why)
	}
	if ref := Install(context.Background(), куда(t, m), m.Pass, новая, known, 5); ref != nil {
		t.Fatalf("заход с новой подписью: %s", ref.Why)
	}
	if n := strings.Count(strings.TrimSpace(m.Authorized()), "\n"); n != 0 {
		t.Fatalf("смена подписи оставила на машине лишнюю строку:\n%s", m.Authorized())
	}

	// И снятие НОВОЙ подписью убирает строку, положенную под СТАРОЙ.
	RemoveByKey(context.Background(), куда(t, m), pair.Private, новая, known, 5)
	if лежит := strings.TrimSpace(m.Authorized()); лежит != "" {
		t.Fatalf("строка со старой подписью пережила снятие:\n%s", лежит)
	}
}

// ПОДПИСЬ НАЗЫВАЕТ СКОУП. Без этого человек, открывший свой `authorized_keys`, видит
// несколько одинаковых строк и не может понять, какая от какого скоупа.
func TestПодписьНазываетСкоуп(t *testing.T) {
	pair, _ := Generate(Sign("http://194.41.113.134:8077/"))
	if !strings.Contains(pair.Authorized, "194.41.113.134:8077") {
		t.Fatalf("подпись не называет скоуп: %s", pair.Authorized)
	}
	if !strings.Contains(pair.Authorized, comment) {
		t.Fatalf("подпись не называет, кто положил строку: %s", pair.Authorized)
	}
	// Адреса нет — подпись прежняя: так подписаны ключи, положенные до этого правила.
	if Sign("") != comment {
		t.Fatalf("без адреса подпись изменилась: %s", Sign(""))
	}
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ УБОРКА ИЗМЕРЯЕТСЯ, А НЕ ВЫВОДИТСЯ ИЗ КОДА ВОЗВРАТА (`WORLD2` 4.2 п. 5).              │
// │                                                                                      │
// │ «Убрали» — это НОЛЬ наших строк в файле после команды, а не «шелл дошёл до конца».    │
// │ Пока это выводилось, снятие могло молча ничего не убрать, и юзер узнавал бы о строке  │
// │ на своей машине, найдя её однажды сам.                                                │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func TestНеУбралосьЭтоСказаноВслухАНеМолчание(t *testing.T) {
	m := машина(t)
	pair, _ := Generate(Sign("http://194.41.113.134:8077/"))
	known := filepath.Join(t.TempDir(), "known_hosts")

	if ref := Install(context.Background(), куда(t, m), m.Pass, pair.Authorized, known, 5); ref != nil {
		t.Fatalf("ключ не лёг: %s", ref.Why)
	}
	// Каталог закрыт на запись: строку не переписать. Файл при этом читается — то есть
	// команда пройдёт до конца, а уберётся ровно ничего.
	ssh := filepath.Join(m.Home, ".ssh")
	if err := os.Chmod(ssh, 0o500); err != nil {
		t.Skipf("каталог не закрыть на запись: %v", err)
	}
	defer os.Chmod(ssh, 0o700)

	ref := RemoveByKey(context.Background(), куда(t, m), pair.Private, pair.Authorized, known, 5)
	if ref == nil {
		if лежит := strings.TrimSpace(m.Authorized()); лежит == "" {
			t.Skip("машина дала переписать файл — измерять нечего")
		}
		t.Fatal("строка осталась на машине, а уборка отчиталась успехом")
	}
	if ref.Code != "key-left" {
		t.Fatalf("осталось названо не своим кодом: %s — %s", ref.Code, ref.Why)
	}
	// ЮЗЕР ОБЯЗАН УЗНАТЬ, КАКАЯ ИМЕННО СТРОКА ОСТАЛАСЬ: подпись содержит адрес скоупа, и
	// только по ней он отличит её от соседних.
	if !strings.Contains(strings.Join(ref.Ways, " "), "194.41.113.134:8077") {
		t.Fatalf("не названо, какую строку убирать руками: %v", ref.Ways)
	}
}

// МАШИНА НАШ КЛЮЧ НЕ ПРИНЯЛА — значит его строки в её файле нет, и это НЕ «осталась».
// Пугать юзера строкой, которой нет, значит учить не верить нашим словам.
func TestКлючНеПринятЗначитСтрокиТамНет(t *testing.T) {
	m := машина(t)
	pair, _ := Generate(Sign("http://проба:8070/"))
	known := filepath.Join(t.TempDir(), "known_hosts")

	if ref := RemoveByKey(context.Background(), куда(t, m), pair.Private, pair.Authorized, known, 5); ref != nil {
		t.Fatalf("уборка на машине, где нашей строки нет, отчиталась бедой: %s — %s", ref.Code, ref.Why)
	}
}

// А ВОТ «НЕ ДОТЯНУЛИСЬ» — ЭТО НЕ «УБРАЛИ». Там мы и правда не знаем, что лежит в файле, и
// сказать «ушли совсем» не вправе.
func TestДоМашиныНеДошлиЗначитСтрокаМоглаОстаться(t *testing.T) {
	pair, _ := Generate(Sign("http://проба:8070/"))
	known := filepath.Join(t.TempDir(), "known_hosts")
	// Порт, на котором никто не слушает: машина ушла, а строка на ней осталась.
	ref := RemoveByKey(context.Background(), Machine{User: "world", Host: "127.0.0.1", Port: 9},
		pair.Private, pair.Authorized, known, 2)
	if ref == nil {
		t.Fatal("до машины не дотянулись, а уборка отчиталась успехом")
	}
	if ref.Code != "key-left" {
		t.Fatalf("названо не тем: %s — %s", ref.Code, ref.Why)
	}
}
