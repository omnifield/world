package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

// Раскладка на диске. Плоская и читаемая глазами намеренно: журнал смотрят и
// ручкой, и `cat`'ом, когда сервис лежит. Поэтому JSONL, а не БД.
const (
	runFile    = "run.json"
	eventsFile = "events.jsonl"
	stateFile  = "state.json"
)

// errNotFound — «такой сессии на диске нет». Наверх уезжает как 404.
var errNotFound = errors.New("сессии нет на диске")

// Store — каталог прогонов. Пишущих может быть несколько (стенд шлёт пачками
// и параллельно), поэтому у каждой сессии свой мьютекс: дописывание не рвёт
// строку и не мешает её с чужой. Очередей и БД для этого не нужно.
type Store struct {
	dir string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New проверяет каталог прогонов ДО старта и падает с адресом на руках.
// Молча подняться и обнаружить некуда писать в момент первого события —
// худший исход: стенд на другой машине, и отказ он увидит как «сервер лежит».
func New(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("путь каталога прогонов не разрешается: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("каталог прогонов не создаётся: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("каталог прогонов не читается: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("каталог прогонов должен быть каталогом, а это файл: %s", abs)
	}
	return &Store{dir: abs, locks: map[string]*sync.Mutex{}}, nil
}

// Dir — куда именно пишем. Нужен трассировке и стартовому логу.
func (s *Store) Dir() string { return s.dir }

// lock выдаёт мьютекс сессии, заводя его при первом обращении.
func (s *Store) lock(session string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.locks[session]
	if !ok {
		m = &sync.Mutex{}
		s.locks[session] = m
	}
	return m
}

func (s *Store) sessionDir(session string) string { return filepath.Join(s.dir, session) }

// SaveRun кладёт манифест прогона. Перезапись целиком: манифест один на прогон.
func (s *Store) SaveRun(session string, body []byte) error {
	m := s.lock(session)
	m.Lock()
	defer m.Unlock()
	return s.replace(session, runFile, append(body, '\n'))
}

// SaveState перезаписывает снимок мира целиком — последний побеждает.
func (s *Store) SaveState(session string, body []byte) error {
	m := s.lock(session)
	m.Lock()
	defer m.Unlock()
	return s.replace(session, stateFile, append(body, '\n'))
}

// replace пишет через временный файл и rename: читатель видит либо старый
// снимок целиком, либо новый целиком, но никогда — половину.
func (s *Store) replace(session, name string, body []byte) error {
	dir := s.sessionDir(session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("каталог сессии не создаётся: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return fmt.Errorf("временный файл не создаётся: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("запись не удалась: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("временный файл не закрывается: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("права на файл не ставятся: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("файл не переименовывается: %w", err)
	}
	return nil
}

// AppendEvents дописывает строки журнала. Существующие строки не
// переписываются НИКОГДА — только O_APPEND, и вся пачка уходит одним Write
// под мьютексом сессии, поэтому чужая пачка не вклинится в середину.
func (s *Store) AppendEvents(session string, lines [][]byte) error {
	m := s.lock(session)
	m.Lock()
	defer m.Unlock()

	dir := s.sessionDir(session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("каталог сессии не создаётся: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, eventsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("журнал не открывается на дозапись: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	for _, line := range lines {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("дозапись не удалась: %w", err)
	}
	return f.Close()
}

// ReadEvents отдаёт строки журнала как они лежат. since>0 оставляет только
// события с бо́льшим seq — так стенд дочитывает журнал с места, где встал.
// Строка без seq считается нулевой и при since>0 не отдаётся.
func (s *Store) ReadEvents(session string, since int64) ([][]byte, error) {
	raw, err := os.ReadFile(filepath.Join(s.sessionDir(session), eventsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("журнал не читается: %w", err)
	}

	var out [][]byte
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if since > 0 {
			var d doc
			// Битую строку не прячем: журнал только дописывался, значит её
			// такой и приняли, и увидеть её должен читатель, а не мы.
			if err := json.Unmarshal(line, &d); err == nil && seqOf(d) <= since {
				continue
			}
		}
		out = append(out, line)
	}
	return out, nil
}

// ReadState отдаёт снимок мира как он лежит.
func (s *Store) ReadState(session string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(s.sessionDir(session), stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("снимок не читается: %w", err)
	}
	return bytes.TrimSpace(raw), nil
}

// ListRuns перечисляет прогоны в порядке имён. Сессия без манифеста в списке
// остаётся: события в ней есть, и молчать о ней хуже, чем показать голое имя.
func (s *Store) ListRuns() ([][]byte, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("каталог прогонов не читается: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && sessionRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := make([][]byte, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(s.dir, name, runFile))
		if err != nil {
			out = append(out, []byte(`{"session":`+strconv.Quote(name)+`}`))
			continue
		}
		out = append(out, bytes.TrimSpace(raw))
	}
	return out, nil
}
