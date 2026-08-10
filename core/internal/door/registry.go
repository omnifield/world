package door

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// errUnknown — «такой локации в поле нет». Наверх уезжает как 404.
var errUnknown = errors.New("локации нет в реестре")

// Registry — состояние поля на диске (`kb:WORLD-28`), а не память человека.
// Один файл JSON: реестр читают и ручкой, и `cat`'ом, когда сервис лежит, а
// строк в нём столько, сколько локаций в поле, — БД тут была бы лишней вещью.
//
// Пережить рестарт — обязанность, а не удобство: дверь, забывающая поле при
// перезапуске, возвращает нас ровно туда, откуда ушли, — к списку, который
// кто-то держит в голове.
type Registry struct {
	file string

	mu     sync.RWMutex
	byName map[string]Location
}

// fileShape — форма файла на диске. Объект, а не голый массив: массив некуда
// расширять, не сломав читателя, а поле у реестра однажды появится.
type fileShape struct {
	Locations []Location `json:"locations"`
}

// Open поднимает реестр с диска. Файла нет — поле пустое, и это НЕ ошибка:
// первая дверь стартует до первой локации (`kb:FUND-5`: дверь самодостаточна).
// Файл есть, но битый — падаем на старте с именем файла и выходом на руках:
// молча стартовать с пустым полем значит потерять маршруты без единого слова.
func Open(file string) (*Registry, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, fmt.Errorf("путь реестра локаций не разрешается: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, fmt.Errorf("каталог реестра локаций не создаётся: %w", err)
	}

	r := &Registry{file: abs, byName: map[string]Location{}}

	raw, err := os.ReadFile(abs)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("реестр локаций не читается: %w", err)
	}

	var f fileShape
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf(
			"реестр локаций %s не разбирается как JSON: %w; почини файл либо удали его — дверь поднимется с пустым полем", abs, err)
	}
	for _, loc := range f.Locations {
		// Файл могли править руками. Имя — это маршрут, поэтому оно
		// проверяется на загрузке теми же правилами, что и на регистрации:
		// иначе через файл в маршрут заезжает то, что через ручку не прошло.
		if e := validName(loc.Name); e != nil {
			return nil, fmt.Errorf("реестр локаций %s: %s", abs, e.Detail)
		}
		if e := validAddr(loc.Addr); e != nil {
			return nil, fmt.Errorf("реестр локаций %s, локация %q: %s", abs, loc.Name, e.Detail)
		}
		r.byName[loc.Name] = loc
	}
	return r, nil
}

// File — где лежит реестр. Нужен стартовому логу: «переживает рестарт»
// проверяется взглядом на файл, а не на слово.
func (r *Registry) File() string { return r.file }

// List отдаёт поле в порядке имён — список за дверью не должен прыгать
// от перезаписи к перезаписи.
func (r *Registry) List() []Location {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Location, 0, len(r.byName))
	for _, loc := range r.byName {
		out = append(out, loc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup — кто стоит за этим именем. Горячий путь маршрутизации, поэтому
// RLock: читателей много (каждый запрос), пишущих единицы.
func (r *Registry) Lookup(name string) (Location, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	loc, ok := r.byName[name]
	return loc, ok
}

// Put кладёт локацию в поле. Возвращает created=false, если это была
// перерегистрация уже стоящей локации (тот же адрес).
//
// Занятость имени решается ЗДЕСЬ, под тем же замком, что и запись: проверка
// снаружи и запись внутри — это гонка, в которой две локации получают один
// маршрут и обе считают, что успешно зарегистрировались.
func (r *Registry) Put(loc Location) (created bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev, exists := r.byName[loc.Name]
	if exists && prev.Addr != loc.Addr {
		return false, errf(http.StatusConflict, "name-taken",
			"имя %q уже занято локацией по адресу %s — возьми другое имя либо сними ту: DELETE /api/locations/%s",
			loc.Name, prev.Addr, loc.Name)
	}
	if exists {
		// Тот же адрес — это та же локация переобъявляется (рестарт,
		// уточнённое описание). Отказывать здесь значило бы требовать
		// снятия перед каждым подъёмом.
		loc.Since = prev.Since
	}

	r.byName[loc.Name] = loc
	if err := r.save(); err != nil {
		// Диск не принял — в памяти тоже не держим: разъехавшийся с файлом
		// реестр переживёт рестарт неправдой.
		if exists {
			r.byName[loc.Name] = prev
		} else {
			delete(r.byName, loc.Name)
		}
		return false, err
	}
	return !exists, nil
}

// Remove снимает локацию: маршрут уходит вместе с ней.
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev, ok := r.byName[name]
	if !ok {
		return errUnknown
	}
	delete(r.byName, name)
	if err := r.save(); err != nil {
		r.byName[name] = prev
		return err
	}
	return nil
}

// save пишет через временный файл и rename: читатель видит либо старый реестр
// целиком, либо новый целиком, но никогда — половину. Вызывается ТОЛЬКО под
// r.mu (иначе на диск уедет состояние из середины чужой правки).
func (r *Registry) save() error {
	out := make([]Location, 0, len(r.byName))
	for _, loc := range r.byName {
		out = append(out, loc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	body, err := json.MarshalIndent(fileShape{Locations: out}, "", "  ")
	if err != nil {
		return fmt.Errorf("реестр локаций не сериализуется: %w", err)
	}
	body = append(body, '\n')

	dir := filepath.Dir(r.file)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(r.file)+".*")
	if err != nil {
		return fmt.Errorf("временный файл реестра не создаётся: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("запись реестра не удалась: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("временный файл реестра не закрывается: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("права на файл реестра не ставятся: %w", err)
	}
	if err := os.Rename(tmp.Name(), r.file); err != nil {
		return fmt.Errorf("реестр не переименовывается: %w", err)
	}
	return nil
}
