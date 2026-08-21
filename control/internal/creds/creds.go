// Пакет creds — КРЕДЫ К МАШИНЕ: ключ или пароль, и что с ними делает контроллер.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПАРОЛЬ — НЕ ТРАНСПОРТ, А СПОСОБ ПОЛУЧИТЬ КЛЮЧ.                                       │
// │                                                                                      │
// │ Докер ходит до чужой машины СИСТЕМНЫМ ssh (`deploy/remote.sh`), а тот пароль          │
// │ неинтерактивно не берёт. Обойти это можно было двумя способами, и оба отвергнуты      │
// │ решением user: подменить `ssh` в `PATH` обёрткой над `sshpass` — хак поверх чужого    │
// │ поведения; написать свой транспорт вместо `docker context` — прямо запрещено          │
// │ (`deploy/remote.sh`: «своего подъёма мы для этого НЕ ПИШЕМ»).                          │
// │                                                                                      │
// │ Поэтому паролем контроллер заходит РОВНО ОДИН раз: кладёт публичный ключ юзера в      │
// │ `~/.ssh/authorized_keys` той машины, а приватный отдаёт в скоуп. Дальше всё идёт по    │
// │ ключу, и транспорт не меняется ни на строку. Это то же, что человек делает            │
// │ `ssh-copy-id`, — только делает это контроллер, и юзер не открывает терминал ни разу.  │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЦЕНА НАЗВАНА ВСЛУХ: КОНТРОЛЛЕР ПИШЕТ В ЧУЖУЮ МАШИНУ.                                 │
// │                                                                                      │
// │ Одна строка в `~/.ssh/authorized_keys` юзера — обратимая, но это изменение ЕГО        │
// │ машины, и человек обязан узнать о нём ДО, а не после. Поэтому про запись сказано в    │
// │ доке зоны, в подсказке процесса, в журнале подъёма и в каждом отказе этого пути.      │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// ПАРОЛЬ ЖИВЁТ РОВНО ОДИН ВЫЗОВ. Он не попадает ни в скоуп, ни в связку контроллера, ни в
// журнал, ни в текст отказа — нигде и никогда. В скоуп уходит КЛЮЧ, который завёл
// контроллер; пароль после установки ключа не нужен вовсе.
//
// ВИД КРЕД НАЗЫВАЕТСЯ ЯВНО, а не угадывается по виду строки. Угаданный вид однажды примет
// ключ за пароль (или наоборот) — и разбираться человек будет с отказом ssh, а не с нашей
// догадкой. Разбор здесь один и в одном месте (`ParseKind`).
package creds

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/omnifield/world/control/internal/refusal"
)

// Kind — вид кред. Их два, как в PuTTY: ключ либо пароль (решение user 2026-08-17).
type Kind string

const (
	// Key — приватный ключ, названный юзером. Кладётся в скоуп и в связку как есть.
	Key Kind = "key"
	// Password — пароль к машине. Транспортом не становится: им контроллер один раз
	// заходит и заводит ключ.
	Password Kind = "password"
)

// comment — подпись в строке `authorized_keys`. Человек, открывший файл своей машины,
// обязан понимать, откуда там эта строка и чем её убрать.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПОДПИСЬ ОБЯЗАНА РАЗЛИЧАТЬ, А НЕ ТОЛЬКО ОБЪЯСНЯТЬ. Пока она была одна на всех, мы      │
// │ обещали юзеру «убрать доступ можно, удалив эту строку» — и обещание было              │
// │ неисполнимым: строк на машине накапливалось с десяток, все подписаны `world-control`, │
// │ и какая от какого скоупа, узнать было нечем (живой прогон 2026-08-20).                │
// │                                                                                       │
// │ Различает АДРЕС СКОУПА: единица личности — адрес, а не машина и не имя (`WORLD2` 3.4, │
// │ «Копий нет — есть адреса»). По нему человек и опознаёт свою строку.                    │
// │                                                                                       │
// │ СРАВНИВАЕМ ПРИ ЭТОМ НЕ ПОДПИСЬ, А КЛЮЧ (`keyPart`): подпись — это слова для человека, │
// │ и если по ней искать, смена слов положила бы на машину ВТОРУЮ строку с тем же ключом. │
// └─────────────────────────────────────────────────────────────────────────────────────┘
const comment = "world-control"

// Sign — подпись строки для названного скоупа. Пустой адрес оставляет прежнюю подпись:
// так подписаны ключи, положенные до этого правила, и переподписывать их нечем — да и
// незачем, ключ от этого не меняется.
func Sign(scopeAddr string) string {
	if strings.TrimSpace(scopeAddr) == "" {
		return comment
	}
	return comment + " " + strings.TrimSpace(scopeAddr)
}

// keyPart — «тип ключ» без подписи. Строки сравниваются ИМ: ключ — это то, что даёт
// доступ, а подпись лишь объясняет человеку, откуда он взялся.
func keyPart(line string) string {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) < 2 {
		return strings.TrimSpace(line)
	}
	return f[0] + " " + f[1]
}

// ParseKind — вид кред, названный явно. Пустое и неизвестное — РАЗНЫЕ отказы: первое
// значит «не сказали», второе «сказали не то», и чинятся они по-разному.
func ParseKind(value string) (Kind, *refusal.Refusal) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case string(Key), "ключ":
		return Key, nil
	case string(Password), "пароль":
		return Password, nil
	case "":
		return "", refusal.New(http.StatusBadRequest, "no-creds-kind",
			"вид кред не назван, а угадывать его по виду строки нельзя: угаданный однажды примет ключ за пароль",
			`назови явно: creds = {"kind":"key","value":"<приватный ключ>"}`,
			`либо паролем: creds = {"kind":"password","value":"<пароль машины>"} — контроллер заведёт ключ сам`)
	default:
		return "", refusal.New(http.StatusBadRequest, "bad-creds-kind",
			fmt.Sprintf("вид кред %q контроллеру неизвестен", value),
			`видов два, как в PuTTY: "key" — свой приватный ключ, "password" — пароль машины`)
	}
}

// Machine — до кого дотягиваемся паролем. Разбор адреса живёт у соседа (`resource`), сюда
// приезжает уже разобранный: второй разбор того же адреса разъехался бы с первым.
type Machine struct {
	User string
	Host string
	Port int
}

func (m Machine) String() string {
	return m.User + "@" + net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
}

// Pair — пара ключей юзера. Приватный уезжает в скоуп, публичный — на машину.
type Pair struct {
	// Private — приватный ключ в формате OpenSSH. Его берёт системный ssh, поэтому формат
	// не наш выбор, а его требование.
	Private string
	// Authorized — ровно та строка, которую кладут в `authorized_keys`.
	Authorized string
}

// Generate заводит новую пару. Ed25519, потому что это сегодняшнее умолчание OpenSSH:
// короткий ключ, без параметров, которые надо выбирать (сверка с рынком 2026-08-17 —
// `ssh-keygen` без ключей заводит ed25519 с OpenSSH 9).
func Generate(sign string) (Pair, *refusal.Refusal) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Pair{}, refusal.New(http.StatusInternalServerError, "no-key",
			"пару ключей завести не вышло: "+err.Error(),
			"попробуй ещё раз",
			"если повторяется — это дефект контроллера, заведи задачу зоне control")
	}
	block, err := ssh.MarshalPrivateKey(priv, sign)
	if err != nil {
		return Pair{}, refusal.New(http.StatusInternalServerError, "no-key",
			"приватный ключ не собрался: "+err.Error(),
			"если повторяется — это дефект контроллера, заведи задачу зоне control")
	}
	signer, err := ssh.NewPublicKey(pub)
	if err != nil {
		return Pair{}, refusal.New(http.StatusInternalServerError, "no-key",
			"публичный ключ не собрался: "+err.Error(),
			"если повторяется — это дефект контроллера, заведи задачу зоне control")
	}
	return Pair{
		Private:    string(pem.EncodeToMemory(block)),
		Authorized: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer))) + " " + sign,
	}, nil
}

// Authorized — строка `authorized_keys` по приватному ключу, который уже лежит в скоупе.
// Публичный ключ в формате не хранится намеренно: он производен от приватного, а второе
// поле того же самого однажды разъехалось бы с первым.
func Authorized(private, sign string) (string, *refusal.Refusal) {
	signer, err := ssh.ParsePrivateKey([]byte(private))
	if err != nil {
		return "", refusal.New(http.StatusBadGateway, "bad-key",
			"ключ из скоупа не разобрать: "+err.Error(),
			"посмотри раздел «ключи» в состоянии: там лежит приватный ключ в формате OpenSSH",
			"либо заведи территорию заново — контроллер заведёт новый ключ")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " " + sign, nil
}

// Install кладёт публичный ключ на машину, зайдя туда ПАРОЛЕМ.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ РЕЗУЛЬТАТ ИЗМЕРЯЕТСЯ, А НЕ ВЫВОДИТСЯ ИЗ КОДА ВОЗВРАТА (`WORLD2` 4.2 п. 5).           │
// │                                                                                      │
// │ Команда на той стороне заканчивается подсчётом СВОИХ строк в `authorized_keys`, и мы  │
// │ требуем ровно одну. Ноль значит «не записалось» — и отчитаться успехом тут нельзя;    │
// │ два и больше — «заход плодит строки», а этого не должно быть никогда.                 │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// knownHosts — куда записать ключ САМОЙ машины. Тот же файл потом читает системный ssh, и
// запись здесь означает, что первая встреча состоялась ровно один раз — при заведении.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПАРОЛЬ ДАЛЬШЕ ЭТОЙ ФУНКЦИИ НЕ ЖИВЁТ, И ЭТО УСТРОЙСТВО, А НЕ ОБЕЩАНИЕ (`WORLD2-143`). │
// │                                                                                      │
// │ Здесь он превращается в способ входа и исчезает: вся работа идёт в `установить`, где  │
// │ такого имени нет вовсе. Значит уронить его в текст отказа НЕЧЕМ — ни в сегодняшний,   │
// │ ни в тот, которого ещё не написали. Раньше пароль был в области видимости всей        │
// │ функции, включая два места, где собирается текст `key-not-installed`: код был чист, а │
// │ дыра открыта, и порча ревью 2026-08-17 прошла зелёной мимо тестов и пробы.            │
// │                                                                                      │
// │ Форма не изобретена: ровно так уже жила уборка (`Remove` → `убрать`), где способ      │
// │ входа приезжает готовым, а пароля внутри нет. Здесь взят её образец.                  │
// │                                                                                      │
// │ Сторож у свойства всё равно стоит — тесты гоняют ВСЕ отказы этого пути и требуют,     │
// │ чтобы значения в них не было; но ловит он теперь ошибку, которую устройство уже не    │
// │ допускает.                                                                            │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func Install(ctx context.Context, m Machine, password, authorized, knownHosts string, timeout int) *refusal.Refusal {
	return установить(ctx, m, ssh.Password(password), authorized, knownHosts, timeout)
}

// установить — вся работа установки, и ПАРОЛЯ ЗДЕСЬ НЕТ: внутрь приезжает способ входа.
// Отказы этого пути строятся ниже, и подставить в них значение неоткуда.
func установить(ctx context.Context, m Machine, auth ssh.AuthMethod, authorized, knownHosts string, timeout int) *refusal.Refusal {
	if timeout <= 0 {
		timeout = 10
	}
	cfg := &ssh.ClientConfig{
		User:            m.User,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: rememberHost(knownHosts),
		Timeout:         time.Duration(timeout) * time.Second,
	}

	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(m.Host, strconv.Itoa(m.Port)))
	if err != nil {
		return reachFailure(m, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(m.Host, strconv.Itoa(m.Port)), cfg)
	if err != nil {
		_ = conn.Close()
		return handshakeFailure(m, err)
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return refusal.New(http.StatusBadGateway, "key-not-installed",
			fmt.Sprintf("на машине %s не открыть сеанс: %v", m, err),
			"проверь, что на ней есть шелл для этого юзера",
			"ключ на машину не положен — ничего не изменено")
	}
	defer session.Close()

	out, err := session.Output(install(authorized))

	// ┌─────────────────────────────────────────────────────────────────────────────────┐
	// │ РЕШАЕТ СЧЁТ, А НЕ КОД ВОЗВРАТА. Команда заканчивается подсчётом НАШИХ строк в     │
	// │ файле, и успех — это ровно одна. Ошибка команды идёт подробностью к отказу, а не  │
	// │ вместо измерения: «код ноль» означает лишь, что шелл дошёл до конца, а записалось  │
	// │ ли что-нибудь — вопрос к файлу (`WORLD2` 4.2 п. 5).                                │
	// └─────────────────────────────────────────────────────────────────────────────────┘
	count := strings.TrimSpace(lastLine(string(out)))
	if count == "1" {
		return nil
	}

	подробность := ""
	if err != nil {
		подробность = ": " + err.Error()
	}
	if n, cerr := strconv.Atoi(count); cerr == nil && n > 1 {
		return refusal.New(http.StatusBadGateway, "key-doubled",
			fmt.Sprintf("на машине %s наш ключ лежит в ~/.ssh/authorized_keys больше одного раза (%d)", m, n),
			"убери лишние строки руками: они помечены подписью "+comment,
			"это дефект контроллера, если строки появились не руками, — заведи задачу зоне control")
	}
	return refusal.New(http.StatusBadGateway, "key-not-installed",
		fmt.Sprintf("на машине %s ключа в ~/.ssh/authorized_keys после записи нет — записать его не вышло%s", m, подробность),
		"чаще всего это права на домашний каталог либо переполненный диск",
		"проверь руками на той машине: ls -ld ~/.ssh && touch ~/.ssh/authorized_keys",
		"на машине при этом ничего не сломано: ключ либо лёг, либо не лёг")
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ Remove — УБРАТЬ СВОЮ СТРОКУ С ЧУЖОЙ МАШИНЫ ЗАХОДОМ ПО ПАРОЛЮ. Зовётся там, где        │
// │ паролем же её и положили: заведение не состоялось, либо мир на этой машине закончил   │
// │ дело и уходит.                                                                         │
// │                                                                                        │
// │ Живой прогон 2026-08-20: заведение падало ПОСЛЕ того, как ключ уже лёг, — и строка    │
// │ оставалась. Скоуп при этом не записался, значит приватного ключа у юзера нет: строку  │
// │ на своей машине он опознать не может и убрать её ему нечем. За несколько попыток их   │
// │ накопилось шесть.                                                                     │
// │                                                                                        │
// │ Отказ не вправе ничего оставлять за собой (`WORLD2` 2.3 п. 5) — а на ЧУЖОЙ машине     │
// │ тем более. Снятие идёт тем же паролем и тем же заходом, что и установка: другого      │
// │ способа у нас нет, а требовать его от юзера значит перекладывать свой мусор на него.  │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func Remove(ctx context.Context, m Machine, password, authorized, knownHosts string, timeout int) *refusal.Refusal {
	if strings.TrimSpace(password) == "" || strings.TrimSpace(authorized) == "" {
		return nil // заходили ключом — на машине ничего нашего не появлялось
	}
	return какОсталось(m, authorized, убрать(ctx, m, ssh.Password(password), authorized, knownHosts, timeout))
}

// RemoveByKey — снять свою строку с машины, ЗАЙДЯ ПО КЛЮЧУ. Зовётся там, где пароля нет и
// быть не должно (он не сохраняется нигде), а ключ есть — он лежит в скоупе, и это ровно
// тот ключ, чью строку мы и убираем.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПОЧЕМУ ЭТО ОБЯЗАТЕЛЬНО. Мы обещаем юзеру: «убрать доступ можно, удалив эту строку».   │
// │ Пока строки копятся, обещание неисполнимо — они все подписаны одинаково, и какая      │
// │ чья, юзер не знает. Живой прогон 2026-08-20: на машине их накопилось восемь.          │
// │                                                                                      │
// │ Снял участок — мир уходит с машины совсем: ни контекста, ни ключа в связке, ни        │
// │ строки в чужом `authorized_keys`. Иначе «снял» значит «почти снял».                    │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func RemoveByKey(ctx context.Context, m Machine, private, authorized, knownHosts string, timeout int) *refusal.Refusal {
	if strings.TrimSpace(private) == "" || strings.TrimSpace(authorized) == "" {
		return nil
	}
	signer, err := ssh.ParsePrivateKey([]byte(private))
	if err != nil {
		return keyLeft(m, authorized, "ключ, которым мы туда ходим, не разобрать: "+err.Error())
	}
	ref := убрать(ctx, m, ssh.PublicKeys(signer), authorized, knownHosts, timeout)
	// ┌─────────────────────────────────────────────────────────────────────────────────┐
	// │ МАШИНА НАШ КЛЮЧ НЕ ПРИНЯЛА — ЗНАЧИТ ЕГО СТРОКИ В ЕЁ ФАЙЛЕ НЕТ, и это тот самый    │
	// │ исход, ради которого мы сюда шли. Лежи она там, ключ пустил бы: тем же ключом      │
	// │ ходит и докер.                                                                     │
	// │                                                                                    │
	// │ Считать это неудачей значило бы пугать юзера строкой, которой нет, — а «не         │
	// │ дотянулись» и «строка осталась» он чинит одинаково и зря. Ступени связи при этом    │
	// │ остаются неудачей: там мы и правда НЕ ЗНАЕМ, что лежит в файле.                     │
	// └─────────────────────────────────────────────────────────────────────────────────┘
	if ref != nil && ref.Code == "access-denied" {
		return nil
	}
	return какОсталось(m, authorized, ref)
}

// какОсталось — ступень связи, пересказанная как «что теперь лежит на машине юзера».
// Ступени (`no-route` · `no-answer` · `host-key-changed`) — про дорогу, а человеку здесь
// важно другое: строка не убрана, и вот она какая.
func какОсталось(m Machine, authorized string, ref *refusal.Refusal) *refusal.Refusal {
	if ref == nil || ref.Code == "key-left" {
		return ref
	}
	return keyLeft(m, authorized, ref.Why)
}

// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ УБОРКА НЕ ОТКАЗЫВАЕТ, НО И НЕ МОЛЧИТ — И ЭТО РАЗНЫЕ ВЕЩИ.                             │
// │                                                                                      │
// │ Раньше здесь было «молча»: своим отказом уборка подменила бы причину, ради которой мы │
// │ сюда вернулись, — и это по-прежнему верно. Но из «не отказывать» не следует «не        │
// │ говорить»: строка осталась на МАШИНЕ ЮЗЕРА, и узнать о ней он обязан от нас, а не      │
// │ найдя её однажды сам. Поэтому здесь возвращается ОТКАЗ КАК СЛОВА: звать его отказом    │
// │ ручки или сказать словами в ответе — решает тот, кто звал.                             │
// │                                                                                        │
// │ РЕЗУЛЬТАТ ИЗМЕРЯЕТСЯ, а не выводится из кода возврата (`WORLD2` 4.2 п. 5): команда на  │
// │ той стороне заканчивается подсчётом НАШИХ строк, и «убрали» — это ровно ноль.          │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func убрать(ctx context.Context, m Machine, auth ssh.AuthMethod, authorized, knownHosts string, timeout int) *refusal.Refusal {
	if timeout <= 0 {
		timeout = 15
	}
	cfg := &ssh.ClientConfig{
		User:            m.User,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: rememberHost(knownHosts),
		Timeout:         time.Duration(timeout) * time.Second,
	}
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(m.Host, strconv.Itoa(m.Port)))
	if err != nil {
		return reachFailure(m, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(m.Host, strconv.Itoa(m.Port)), cfg)
	if err != nil {
		_ = conn.Close()
		// Ступени те же, что у установки, и коды те же: «не пустили» отличается от «не
		// дотянулись» и здесь — от первого зависит, знаем ли мы, что лежит в файле.
		return handshakeFailure(m, err)
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return keyLeft(m, authorized, "сеанс не открылся: "+err.Error())
	}
	defer session.Close()

	out, err := session.Output(remove(authorized))
	// РЕШАЕТ СЧЁТ, А НЕ КОД ВОЗВРАТА — как и при установке. Ошибка команды идёт
	// подробностью, а не вместо измерения: «код ноль» говорит лишь, что шелл дошёл до конца.
	if strings.TrimSpace(lastLine(string(out))) == "0" {
		return nil
	}
	подробность := "строка после уборки в файле осталась"
	if err != nil {
		подробность += ": " + err.Error()
	}
	return keyLeft(m, authorized, подробность)
}

// keyLeft — НАША СТРОКА ОСТАЛАСЬ НА ЧУЖОЙ МАШИНЕ, и юзеру названо, какая именно.
//
// Подпись содержит АДРЕС СКОУПА (`Sign`), и это здесь главное: строк на машине может быть
// несколько, а убрать человеку надо ту самую. Обещание «убрать доступ можно, удалив эту
// строку» исполнимо ровно настолько, насколько он может её опознать.
func keyLeft(m Machine, authorized, почему string) *refusal.Refusal {
	way := "убери её руками на той машине: строка в ~/.ssh/authorized_keys"
	if подпись := signOf(authorized); подпись != "" {
		way += ", подписанная «" + подпись + "»"
	}
	return refusal.New(http.StatusBadGateway, "key-left",
		fmt.Sprintf("на машине %s осталась наша строка в ~/.ssh/authorized_keys — %s", m, почему),
		way,
		"или повтори то же действие, когда машина ответит: уборка идёт по ключу, а не по подписи")
}

// signOf — подпись строки, то есть всё, что стоит ПОСЛЕ ключа. Ею человек и опознаёт свою
// строку среди чужих; сравнивать по ней нельзя (`keyPart`), а называть — только ею.
func signOf(line string) string {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) < 3 {
		return ""
	}
	return strings.Join(f[2:], " ")
}

// remove — обратная команда к `install`. Убирает РОВНО нашу строку и ничего кроме: файл
// принадлежит юзеру, и всё остальное в нём — не наше дело. Через временный файл рядом,
// потому что перенаправление в тот же файл обнулило бы его до чтения.
func remove(authorized string) string {
	// Убираем ПО КЛЮЧУ, а не по строке целиком: подписи у одного и того же ключа могли
	// меняться, и строка со старой подписью иначе осталась бы на машине навсегда.
	line := quote(keyPart(authorized))
	return strings.Join([]string{
		"umask 077",
		"if [ -f ~/.ssh/authorized_keys ]; then",
		"  grep -vF -- " + line + " ~/.ssh/authorized_keys > ~/.ssh/authorized_keys.world-tmp",
		"  mv ~/.ssh/authorized_keys.world-tmp ~/.ssh/authorized_keys",
		"  chmod 600 ~/.ssh/authorized_keys",
		"fi",
		// Счёт печатается ВСЕГДА, и ноль печатается тоже: `grep -c` на ноль совпадений
		// выходит единицей, а на отсутствующем файле — двойкой и молчанием. Молчание здесь
		// неотличимо от «команда не дошла», а различать их надо: файла нет — значит нашей
		// строки в нём нет, и это НОЛЬ, а не неизвестность.
		"grep -cF -- " + line + " ~/.ssh/authorized_keys 2>/dev/null || printf '0\\n'",
	}, "\n")
}

// lastLine — последняя непустая строка ответа. Считает наши строки ПОСЛЕДНЯЯ команда, а до
// неё шелл мог сказать своё (например, про несозданный каталог), и это его право.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// install — команда, которая идёт на ту сторону. Одна, и она ИДЕМПОТЕНТНА: тот же ключ
// второй раз не дописывается. Заход, плодящий строки, однажды оставил бы в чужом файле
// десяток наших ключей, и убирать их пришлось бы человеку руками.
func install(authorized string) string {
	line := quote(authorized)
	// Ищем ключ, а кладём строку с подписью: подпись — слова для человека, и по ней искать
	// нельзя. Смена слов иначе положила бы ВТОРУЮ строку с тем же ключом.
	ключ := quote(keyPart(authorized))
	return strings.Join([]string{
		"umask 077",
		"mkdir -p ~/.ssh",
		"touch ~/.ssh/authorized_keys",
		"chmod 700 ~/.ssh",
		"chmod 600 ~/.ssh/authorized_keys",
		"grep -qF -- " + ключ + " ~/.ssh/authorized_keys || printf '%s\\n' " + line + " >> ~/.ssh/authorized_keys",
		// Счёт печатается ВСЕГДА: `grep -c` на ноль совпадений выходит единицей, и без
		// `|| true` «ноль строк» было бы неотличимо от «команда упала». А различать их
		// надо: в первом случае мы точно знаем, что не записалось.
		"grep -cF -- " + ключ + " ~/.ssh/authorized_keys 2>/dev/null || true",
	}, "\n")
}

// ErrHostKeyChanged — по адресу отвечает машина с ДРУГИМ ключом, чем записано. Отдельной
// ошибкой, а не текстом: её разбирает отказ, и от неё зависит, что человеку делать.
var ErrHostKeyChanged = errors.New("ключ хоста изменился")

// rememberHost — ключ САМОЙ машины. Незнакомый — запоминаем при первой встрече (это тот же
// порядок, каким живёт системный ssh с `StrictHostKeyChecking=accept-new`). ИЗМЕНИВШИЙСЯ —
// отказываем.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПОЧЕМУ ОТКАЗ, А НЕ ЗАПИСЬ ВТОРОЙ СТРОКОЙ. Раньше здесь было «не записалось — не       │
// │ отказ», и любой ключ, включая подменённый, принимался МОЛЧА. Два следствия, оба       │
// │ настоящие и оба вылезли живьём 2026-08-20:                                            │
// │                                                                                       │
// │   безопасность  паролем мы ходим ОДИН раз и кладём на ту сторону ключ юзера. Если по  │
// │                 адресу отвечает не та машина, ключ ляжет ей — молча;                  │
// │   рассогласование  системный `ssh`, которым потом идёт подъём, такой ключ ОТВЕРГАЕТ.  │
// │                 Слой заходил, слой подъёма — нет, и человек получал «креды не         │
// │                 приняты» про пароль, который никто не отвергал.                       │
// │                                                                                       │
// │ Оба слоя обязаны отвечать на подмену ОДИНАКОВО. Здесь — тот же ответ, что у ssh.      │
// └─────────────────────────────────────────────────────────────────────────────────────┘
func rememberHost(path string) ssh.HostKeyCallback {
	return func(hostport string, remote net.Addr, key ssh.PublicKey) error {
		if path == "" {
			return nil
		}
		// Сверяем с записанным ДО того, как принять. Незнакомый хост `knownhosts` называет
		// той же ошибкой, что и подменённый, — различает их непустой `Want`: он и есть
		// «а вот что мы помним про этот адрес».
		if _, err := os.Stat(path); err == nil {
			if сверить, err := knownhosts.New(path); err == nil {
				err := сверить(hostport, remote, key)
				if err == nil {
					return nil // тот же ключ, что и был
				}
				var ke *knownhosts.KeyError
				if errors.As(err, &ke) && len(ke.Want) > 0 {
					return ErrHostKeyChanged
				}
				// хост незнаком — запоминаем ниже
			}
		}
		line := knownhosts.Line([]string{knownhosts.Normalize(hostport)}, key) + "\n"
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), strings.TrimSpace(line)) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil
		}
		defer f.Close()
		_, _ = f.WriteString(line)
		return nil
	}
}

// ── отказы ───────────────────────────────────────────────────────────────────
//
// Ступени те же, что во всей зоне: дорога · ответ · доступ. Чинят их разные люди, и общий
// отказ «не дотянулись» отправляет человека чинить наугад (`WORLD2` 2.3).
//
// НИ В ОДНОМ отказе этого файла нет пароля. Он не подставляется в текст ни при каких
// обстоятельствах: отказ читают люди, и он же уезжает в журнал.

func reachFailure(m Machine, err error) *refusal.Refusal {
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns):
		return refusal.New(http.StatusBadGateway, "no-route",
			fmt.Sprintf("имя %s не разрешается — до машины нет дороги", m.Host),
			"проверь адрес машины",
			"пароль никуда не ушёл: соединение не состоялось")
	case errors.Is(err, syscall.ECONNREFUSED):
		return refusal.New(http.StatusBadGateway, "no-answer",
			fmt.Sprintf("дорога до %s есть, а ssh на порту %d не отвечает", m.Host, m.Port),
			"проверь, что ssh на той машине поднят",
			"если порт не 22 — назови его в адресе: user@машина:2222")
	default:
		return refusal.New(http.StatusGatewayTimeout, "no-answer",
			fmt.Sprintf("до ssh на %s дотянуться не вышло: %v", m, err),
			"проверь адрес и что машина жива",
			"дай больше времени: CONTROL_SSH_TIMEOUT=30")
	}
}

func handshakeFailure(m Machine, err error) *refusal.Refusal {
	text := err.Error()
	// ПОДМЕНА МАШИНЫ — НЕ ПРОБЛЕМА КРЕД. Раньше это доезжало как «креды не приняты», и
	// человек шёл чинить пароль, который никто не отвергал (живой прогон 2026-08-20).
	if errors.Is(err, ErrHostKeyChanged) || strings.Contains(text, ErrHostKeyChanged.Error()) {
		return refusal.New(http.StatusBadGateway, "host-key-changed",
			fmt.Sprintf("по адресу %s отвечает машина с ДРУГИМ ключом, чем контроллер запомнил", m),
			"машину переустанавливали — забудь её прежний ключ: docker exec world-control ssh-keygen -R "+m.Host,
			"машину НЕ переустанавливали — значит по этому адресу отвечает не она, и пароль ей давать нельзя",
			"это НЕ «пароль не принят»: до пароля дело не дошло — мы не признали саму машину")
	}
	if strings.Contains(text, "unable to authenticate") || strings.Contains(text, "no supported methods") {
		return refusal.New(http.StatusForbidden, "access-denied",
			fmt.Sprintf("машина %s пароль не приняла", m),
			"проверь пароль и юзера в адресе: ходим именно им",
			"если на машине запрещён вход по паролю (PasswordAuthentication no) — заведи ключ руками и назови его видом «key»")
	}
	return refusal.New(http.StatusBadGateway, "no-answer",
		fmt.Sprintf("ssh на %s не договорился с контроллером: %v", m, err),
		"проверь, что по этому адресу и правда ssh",
		"подробность выше — она от ssh, а не от мира")
}

// quote — единственный способ, которым значение попадает в команду на той стороне. Строка
// ключа приходит от нас, но правило одно на всю зону: в чужую оболочку значения уезжают
// закавыченными (грабля `WORLD2-96`, пункт 2).
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
