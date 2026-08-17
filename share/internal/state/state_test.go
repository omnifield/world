package state

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func store(t *testing.T) *Store {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "scope.json"), 0)
}

func TestReadОтказываетNoStateПокаНичегоНеЗаписали(t *testing.T) {
	_, ref := store(t).Read()
	if ref == nil {
		t.Fatal("на пустом месте прочиталось состояние — такого файла нет")
	}
	if ref.Code != "no-state" {
		t.Fatalf("код отказа %q вместо no-state", ref.Code)
	}
	if len(ref.Ways) == 0 {
		t.Fatal("отказ без выхода — тупик (`WORLD2` 2.3)")
	}
}

// Главное свойство зоны: что положили — то и отдали, байт в байт. Ни разбора, ни
// переупаковки, ни «поправим отступы» (`WORLD2` 3.4).
func TestОтдаётРовноТоЧтоПриняли(t *testing.T) {
	s := store(t)
	// Нарочно НЕ JSON и с не-ASCII: раздача внутрь не смотрит, и доказывается это тем,
	// что она берёт то, чего никакой разбор не принял бы.
	data := []byte("это вообще не JSON \x00\x01 и раздаче всё равно\n")
	if _, ref := s.Write(bytes.NewReader(data)); ref != nil {
		t.Fatalf("запись отказала: %v", ref)
	}
	got, ref := s.Read()
	if ref != nil {
		t.Fatalf("чтение отказало: %v", ref)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("отдано %q вместо %q", got, data)
	}
}

// Цена названа вслух в каноне: пишущий затирает предыдущего целиком. Проба стережёт
// именно это — чтобы никто не «починил» его слиянием, не переоткрывая решение.
func TestЗаписьЗатираетПредыдущуюЦеликом(t *testing.T) {
	s := store(t)
	if _, ref := s.Write(strings.NewReader(`{"формат":1,"личность":{"имя":"первый"}}`)); ref != nil {
		t.Fatalf("первая запись отказала: %v", ref)
	}
	if _, ref := s.Write(strings.NewReader(`{"формат":1}`)); ref != nil {
		t.Fatalf("вторая запись отказала: %v", ref)
	}
	got, ref := s.Read()
	if ref != nil {
		t.Fatalf("чтение отказало: %v", ref)
	}
	if string(got) != `{"формат":1}` {
		t.Fatalf("после второй записи лежит %q — запись не заменила файл целиком", got)
	}
}

func TestWriteОтказываетКодом(t *testing.T) {
	cases := []struct {
		имя  string
		тело string
		код  string
	}{
		{"ноль байт — это стёртая личность, а не пустая", "", "empty-state"},
		{"больше предела", strings.Repeat("я", 500), "state-too-big"},
	}
	for _, c := range cases {
		s := New(filepath.Join(t.TempDir(), "scope.json"), 64)
		_, ref := s.Write(strings.NewReader(c.тело))
		if ref == nil {
			t.Fatalf("%s: отказа не было", c.имя)
		}
		if ref.Code != c.код {
			t.Fatalf("%s: код %q вместо %q", c.имя, ref.Code, c.код)
		}
		if len(ref.Ways) == 0 {
			t.Fatalf("%s: отказ без выхода", c.имя)
		}
	}
}

// Ровно предел — это ещё не «больше предела». Граница на единицу мимо отказывала бы на
// файле, который влезает, и человек чинил бы то, что не сломано.
func TestРовноПределПринимается(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "scope.json"), 8)
	if _, ref := s.Write(strings.NewReader("12345678")); ref != nil {
		t.Fatalf("ровно предел отказал: %v", ref)
	}
	if _, ref := s.Write(strings.NewReader("123456789")); ref == nil {
		t.Fatal("предел с хвостом прошёл — предела нет")
	}
}

// Отказ ничего не ломает (`WORLD2` 2.3 п. 5): состояние остаётся прежним, а времянки не
// копятся рядом с личностью.
func TestОтказНеТрогаетПрежнееСостояниеИНеОставляетВремянок(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "scope.json"), 64)
	if _, ref := s.Write(strings.NewReader("прежнее")); ref != nil {
		t.Fatalf("первая запись отказала: %v", ref)
	}
	if _, ref := s.Write(strings.NewReader(strings.Repeat("x", 100))); ref == nil {
		t.Fatal("запись сверх предела прошла")
	}
	got, ref := s.Read()
	if ref != nil {
		t.Fatalf("чтение после отказа отказало: %v", ref)
	}
	if string(got) != "прежнее" {
		t.Fatalf("после отказа лежит %q — отказ тронул состояние", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "scope.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("рядом с состоянием лежит лишнее: %v", names)
	}
}

// Каталог заводится сам: раздача поднимается на пустой машине, и первый же PUT обязан
// лечь, а не отказать «нет каталога» — заводить его человеку руками негде, он на ту
// машину не заходит вовсе.
func TestКаталогЗаводитсяСам(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "поглубже", "и-ещё", "scope.json"), 0)
	if _, ref := s.Write(strings.NewReader("состояние")); ref != nil {
		t.Fatalf("запись в незаведённый каталог отказала: %v", ref)
	}
	if _, ref := s.Read(); ref != nil {
		t.Fatalf("чтение отказало: %v", ref)
	}
}

func TestПраваНаФайлЛичностиТолькоСвои(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "scope.json"), 0)
	if _, ref := s.Write(strings.NewReader("состояние")); ref != nil {
		t.Fatalf("запись отказала: %v", ref)
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("права на состояние %o — личность юзера открыта не только ему", perm)
	}
}
