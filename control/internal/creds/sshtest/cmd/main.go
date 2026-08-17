// Подставная машина с ssh, поднятая отдельным процессом — ею пользуется ПРОБА зоны.
//
// Тестам ручек хватает машины в своём процессе (`sshtest.Start`); пробе — нет: она
// поднимает настоящий бинарь контроллера, и машина, до которой он дотянется, обязана быть
// снаружи. Отсюда этот файл: ровно запуск и ожидание, вся суть — в пакете рядом.
//
//	машина <адрес> <юзер> <пароль> <домашний каталог>
//
// В выпуск он не едет: на него не смотрит ни один путь из `cmd/control`.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/omnifield/world/control/internal/creds/sshtest"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "машина <адрес> <юзер> <пароль> <домашний каталог>")
		os.Exit(2)
	}
	m, err := sshtest.StartOn(os.Args[1], os.Args[2], os.Args[3], os.Args[4])
	if err != nil {
		fmt.Fprintf(os.Stderr, "подставная машина не поднялась: %v\n", err)
		os.Exit(1)
	}
	defer m.Close()

	fmt.Println(m.Addr)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
}
