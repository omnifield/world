// Пакет pult — раздача лица для человека тем же адресом, что и ручки.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ПОЧЕМУ ПУЛЬТ РАЗДАЁТ КОНТРОЛЛЕР, А НЕ ДВЕРЬ                                          │
// │                                                                                      │
// │ Лицо для человека живёт ПРИ КОНТРОЛЛЕРЕ (`WORLD2` 3.7), дверь не показывает ничего   │
// │ (`5.1`). Один источник снимает вопрос «а откуда приехал пульт» вместе с метками,     │
// │ чужими origin и настройкой на стороне человека: страница и ручки — один адрес.       │
// │                                                                                      │
// │ Рисовать лицо зона `control` по-прежнему НЕ БЕРЁТСЯ — она его только отдаёт. Файлы   │
// │ приезжают собранными из зоны `web`, и ни строки их содержимого здесь нет.            │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Пульта в сборке может не оказаться — и это единственный случай, ради которого пакет
// вообще устроен сложнее двух строк с `http.FileServer`. Пустая страница или голое 404
// стоят человеку часа: он ищет опечатку в адресе, а причина в том, что образ собрали без
// первого шага. Поэтому каждое такое состояние названо кодом, причиной и выходом
// (`WORLD2` 2.3), а не оставлено на догадку.
package pult

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/omnifield/world/control/internal/refusal"
)

// sourceMark — по чему видно, что вместо СБОРКИ подложен исходник зоны `web`. Её
// `index.html` ссылается на `/src/main.tsx`, которого в собранном виде нет: страница
// откроется и не покажет ничего. Отдать её молча — та же беда, что и пустой каталог,
// только незаметнее. Та же проверка есть у двери (`core`), и это не дубль правила, а
// одинаковая защита от одной и той же ошибки раскладки в двух местах, где она возможна.
const sourceMark = "/src/main.tsx"

// assetsPrefix — каталог, куда сборщик кладёт файлы с хэшем в имени. Только для них
// уместен долгий кэш: имя меняется вместе с содержимым.
const assetsPrefix = "/assets/"

// Handler — раздача собранного пульта из каталога.
type Handler struct {
	// Dir — где лежит СОБРАННЫЙ пульт. Значение, а не константа: в образе это
	// `/opt/world/pult`, в разработке — `../web/dist`, в пробе — временный каталог.
	Dir string
}

func New(dir string) *Handler { return &Handler{Dir: dir} }

// State — одна строка для стартового журнала: что человек получит, постучавшись в `/`.
// Состояние проверяется НА СТАРТЕ, чтобы сказать вслух сразу, и ещё раз на каждом запросе,
// потому что в разработке каталог пересобирается под работающим процессом.
func (h *Handler) State() string {
	if ref := h.check(); ref != nil {
		return "ВЫКЛЮЧЕН — " + ref.Why
	}
	return "из " + h.Dir
}

// Serve отдаёт файл пульта либо возвращает отказ. Печатает отказ не он: форма ответа —
// дело ручек (`internal/api`), и второй способ сказать «нет» здесь не заводится.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request) *refusal.Refusal {
	// Метод проверяем сами: `http.ServeContent` отдаёт файл на любой глагол, а `POST` в
	// страницу — это не запрос содержимого, а промах мимо ручки, и назвать его надо
	// промахом.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return refusal.New(http.StatusMethodNotAllowed, "wrong-method",
			"пульт отдаётся по GET и HEAD, а пришёл "+r.Method,
			"если это запрос к ручке — они живут под /api/, список в control/README.md")
	}
	if ref := h.check(); ref != nil {
		return ref
	}

	// Единственное место, где путь из запроса превращается в путь на диске. `path.Clean`
	// от «/» + путь съедает `..` до того, как мы что-либо откроем: без этого запрос
	// `/../../etc/passwd` уехал бы читать чужие файлы правами контроллера, а прав у него
	// много.
	clean := path.Clean("/" + r.URL.Path)
	if clean == "/" {
		clean = "/index.html"
	}
	name := filepath.Join(h.Dir, filepath.FromSlash(clean))

	file, err := os.Open(name)
	if err != nil {
		return h.noPage(clean)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return h.noPage(clean)
	}
	if info.IsDir() {
		// Каталог не листается НИКОГДА. Список файлов — это карта того, что внутри
		// образа, и отдавать её тому, кто просто ошибся адресом, незачем.
		return h.noPage(clean)
	}

	// Кэш назначается по имени, а не «на всякий случай»: у файлов сборки хэш в имени, и
	// они не меняются никогда; `index.html` меняется каждой сборкой, и закэшированный он
	// показывал бы человеку прошлый пульт при живом новом.
	if strings.HasPrefix(clean, assetsPrefix) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	return nil
}

// check — состояние пульта. Причина каждого «нет» здесь своя: пустой каталог, подложенный
// исходник и нечитаемый файл чинятся разными действиями.
func (h *Handler) check() *refusal.Refusal {
	if strings.TrimSpace(h.Dir) == "" {
		return refusal.New(http.StatusServiceUnavailable, "no-pult",
			"каталог пульта не назван, а угадать его нельзя",
			"назови: CONTROL_PULT=/opt/world/pult (в образе он лежит там)",
			"в разработке — собранный пульт зоны web: CONTROL_PULT=../web/dist")
	}

	index := filepath.Join(h.Dir, "index.html")
	data, err := os.ReadFile(index)
	switch {
	case os.IsNotExist(err):
		return refusal.New(http.StatusServiceUnavailable, "no-pult",
			fmt.Sprintf("пульта в этой сборке нет: в %s не лежит index.html", h.Dir),
			"собери его первым шагом: ./deploy/build.sh --only-web",
			"и пересобери образ контроллера: ./control/up.sh",
			"ручки при этом работают: curl -s <адрес>/api/me")
	case err != nil:
		return refusal.New(http.StatusServiceUnavailable, "pult-unreadable",
			fmt.Sprintf("пульт лежит в %s, но прочитать его не вышло: %v", h.Dir, err),
			"проверь права на каталог внутри контроллера",
			"пересобери образ: ./control/up.sh")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return refusal.New(http.StatusServiceUnavailable, "no-pult",
			fmt.Sprintf("в %s лежит пустой index.html — это не собранный пульт", h.Dir),
			"собери его первым шагом: ./deploy/build.sh --only-web",
			"и пересобери образ контроллера: ./control/up.sh")
	}
	if bytes.Contains(data, []byte(sourceMark)) {
		return refusal.New(http.StatusServiceUnavailable, "pult-not-built",
			fmt.Sprintf("в %s лежит ИСХОДНИК зоны web, а не её сборка: index.html ссылается на %s, которого в собранном виде нет", h.Dir, sourceMark),
			"назови каталог сборки, а не зону: CONTROL_PULT=../web/dist",
			"сборки ещё нет — собери: ./deploy/build.sh --only-web")
	}
	return nil
}

func (h *Handler) noPage(clean string) *refusal.Refusal {
	return refusal.New(http.StatusNotFound, "unknown-page",
		"такой страницы у пульта нет: "+clean,
		"лицо мира живёт по адресу /",
		"ручки — под /api/, список в control/README.md")
}
