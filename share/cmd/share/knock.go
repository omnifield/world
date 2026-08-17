package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Стук — жива ли раздача и отвечает ли она СВОИМ языком.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ТРЕТЬЕЙ РУЧКИ РАДИ ПРОВЕРКИ МЫ НЕ ЗАВОДИМ (`WORLD2` 3.4). Стук идёт в ту же `GET /`,  │
// │ но БЕЗ пароля — и ждёт `401 no-creds`. Это полноценный ответ, а не поломка            │
// │ (`WORLD2` 2.3: каждое «нет» приходит тройкой), и по нему видно ровно то, что нужно:   │
// │ процесс жив, слушает и отвечает раздачей, а не чем-то ещё на этом порту.              │
// │                                                                                      │
// │ Проверка «порт открыт» зеленела бы на повисшем процессе. Проверка «код 200»           │
// │ потребовала бы пароль в HEALTHCHECK и краснела бы на свежей раздаче, где состояния    │
// │ ещё нет, — то есть ровно в законном состоянии (`no-state`).                           │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Стук — команда САМОГО БИНАРЯ, а не отдельный скрипт: в образе нет ни curl, ни bash, и
// тянуть их ради проверки значило бы класть в поставку инструмент, которым больше никто
// не пользуется.
func knock() {
	addr := env("SHARE_ADDR", defaultAddr)
	timeout := time.Duration(number("SHARE_KNOCK_TIMEOUT", 3)) * time.Second

	// Стучим В ПЕТЛЮ, а не по объявленному адресу: `:8070` означает «на всех адресах», и
	// подставлять туда чужой хост нечего — проверяем мы свой процесс, изнутри.
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = strings.TrimPrefix(addr, ":")
	}
	url := "http://127.0.0.1:" + port + "/"

	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "стук: раздача по %s не ответила: %v\n", url, err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		fmt.Fprintf(os.Stderr, "стук: на %s ответило %d — на этом порту не раздача\n", url, resp.StatusCode)
		os.Exit(1)
	}
	// Статуса мало: `401` отдаст и посторонний сторож перед портом. Ждём СВОЙ код отказа —
	// он и означает «отвечает раздача».
	var ref struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil || ref.Code != "no-creds" {
		fmt.Fprintf(os.Stderr, "стук: на %s отказали, но не по-нашему (code=%q)\n", url, ref.Code)
		os.Exit(1)
	}
}
