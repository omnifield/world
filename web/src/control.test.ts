// Пробы разговора с контроллером.
//
// `WORLD2` 4.2: проба, которую ни разу не заставили упасть, пробой не является. Поэтому
// счастливый путь здесь — один тест из многих, а каждый отказ вызывается НАРОЧНО: сеть легла,
// тело не того вида, форма ответа разошлась с контрактом. Проверяем при этом не «покраснело»,
// а что именно сказано человеку: отказ без причины и выходов — провал, а не мелочь
// (`WORLD2` 2.3).
//
// ОБРАЗЦЫ ОТВЕТОВ ЗДЕСЬ НЕ СОЧИНЯЮТСЯ: они вырезаны из контракта соседа (`control/README.md`
// через `probe-contract.ts`). Написанный рукой образец делает пробу зелёной ровно до тех пор,
// пока сам себе не соврёт: `door`/`alive` у контроллера не стало (`WORLD2-131`), а здешние
// пробы об этом молчали — они разбирали нашу же выдумку.
import { describe, expect, it } from "vitest";

import { type Fetcher, PATH, type Resource, type Session, liveControl } from "./control.js";
import { СВЕЖИЙ_СКОУП, изКонтракта } from "./probe-contract.js";

/** Один запрос, как его увидел контроллер: путь, глагол, заголовки, тело. */
type Записанный = {
  path: string;
  method: string;
  headers: Record<string, string>;
  body: unknown;
};

/**
 * Контроллер для пробы. Своими руками, а не через глобальный Response: клиент держится ровно
 * за `ok`/`status`/`statusText`/`text()`, и подделка ровно этих четырёх делает видимым, какой
 * поверхностью ответа пульт вообще пользуется.
 */
function отвечает(
  ответы: Array<{ body: string; status?: number; statusText?: string }>,
): { fetch: Fetcher; записи: Записанный[] } {
  const записи: Записанный[] = [];
  let n = 0;

  const fetch: Fetcher = async (path, init) => {
    const шаг = ответы[Math.min(n, ответы.length - 1)];
    n += 1;
    записи.push({
      path,
      method: init?.method ?? "GET",
      headers: { ...((init?.headers as Record<string, string>) ?? {}) },
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    });
    const status = шаг.status ?? 200;
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: шаг.statusText ?? "",
      text: async () => шаг.body,
    } as unknown as Response;
  };

  return { fetch, записи };
}

/** Ответ входа — образец из контракта («Ответ входа»). `since` у него нет: его нет и у ручки. */
const ВХОД = изКонтракта<Omit<Session, "since">>("token");

/**
 * Источники ресурса — образец оттуда же. В нём УЖЕ есть оба ответа про вещи: у «здесь» список,
 * у молчащего `null`. Это не совпадение, а то самое место контракта, ради которого он и написан
 * так: «не спросили» и «пусто» — разные ответы.
 */
const [ЗДЕСЬ, ВТОРОЙ] = изКонтракта<{ resources: Resource[] }>("resources").resources;

/** Отказ контроллера — образец оттуда же («Отказ — код, причина, выход»). */
const ОТКАЗ = изКонтракта<{ code: string; why: string; ways: string[] }>("code");

describe("вход", () => {
  it("отдаёт личность и скоуп в том виде, в каком их назвал контроллер", async () => {
    const { fetch, записи } = отвечает([{ body: JSON.stringify(ВХОД) }]);
    const answer = await liveControl(fetch).enter({ addr: "/scope/егор" });

    expect(answer).toEqual({
      kind: "ok",
      value: { ...ВХОД, since: "" },
    });
    expect(записи[0]?.path).toBe(PATH.session);
    expect(записи[0]?.method).toBe("POST");
    expect(записи[0]?.body).toEqual({ addr: "/scope/егор" });
  });

  it("«завести здесь» уходит отдельным полем, а не догадкой по пустому ответу", async () => {
    // Завести личность молча, потому что «ничего не нашлось», значит однажды завести её на
    // опечатке в адресе. Поэтому `create` — явное поле, и проба стережёт именно его.
    const { fetch, записи } = отвечает([
      { body: JSON.stringify({ ...ВХОД, created: true }), status: 201 },
    ]);
    const answer = await liveControl(fetch).enter({
      addr: "/scope/егор",
      create: true,
      name: "егор",
      brand: "омнифилд",
    });

    expect(answer.kind === "ok" && answer.value.created).toBe(true);
    expect(записи[0]?.body).toEqual({
      addr: "/scope/егор",
      create: true,
      name: "егор",
      brand: "омнифилд",
    });
  });

  it("метка сессии едет заголовком в следующие запросы, а не только печеньем", async () => {
    // Печенье контроллер ставит сам, но полагаться только на него нельзя: пульт может
    // раздаваться не с того origin, где стоит контроллер, — этот шов ещё не решён.
    const { fetch, записи } = отвечает([
      { body: JSON.stringify(ВХОД) },
      { body: JSON.stringify({ resources: [ЗДЕСЬ] }) },
    ]);
    const control = liveControl(fetch);
    await control.enter({ addr: "/scope/егор" });
    await control.resources();

    expect(записи[0]?.headers.Authorization).toBeUndefined();
    expect(записи[1]?.headers.Authorization).toBe(`Bearer ${ВХОД.token}`);
  });
});

describe("свежесозданный скоуп — первое, что видит новый юзер", () => {
  // `WORLD2-135`: пульт требовал непустой бренд и не пускал в мир НИКОГО, кто входит впервые.
  // Ни одна из проб зоны этого не поймала, и сверка с образцом контракта не поймала бы тоже:
  // пример в доке показывает форму, а не законный край значений — бренд там назван.
  it("вход с ПУСТЫМ брендом проходит: пустое законно, а не поломка ответа", async () => {
    const { fetch } = отвечает([{ body: JSON.stringify(СВЕЖИЙ_СКОУП), status: 201 }]);
    const answer = await liveControl(fetch).enter({
      addr: СВЕЖИЙ_СКОУП.scope.addr,
      create: true,
      name: СВЕЖИЙ_СКОУП.name,
    });

    expect(answer.kind).toBe("ok");
    expect(answer.kind === "ok" && answer.value.brand).toBe("");
    expect(answer.kind === "ok" && answer.value.created).toBe(true);
  });

  it("пустой бренд ничем не подменяется — ни именем, ни прочерком", async () => {
    // Мир не выдумывает за юзера (`WORLD2` 3.7, 0.1): пустое доезжает пустым, а называется
    // словами уже на экране.
    const { fetch } = отвечает([{ body: JSON.stringify(СВЕЖИЙ_СКОУП) }]);
    const answer = await liveControl(fetch).me();

    expect(answer.kind === "ok" && answer.value.brand).toBe("");
  });

  it("а вот бренда НЕ СТРОКОЙ — это уже форма, и она отказ", async () => {
    // «Поле пустое» и «поля нет» — разные вещи. Первое законно, второе значит, что контракт
    // разъехался, и разбирать такой ответ дальше нельзя.
    const { brand: _, ...без } = СВЕЖИЙ_СКОУП;
    const { fetch } = отвечает([{ body: JSON.stringify(без) }]);
    const answer = await liveControl(fetch).me();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
    expect(answer.kind === "refusal" && answer.refusal.why).toContain("brand");
  });

  it("а вот пустое ИМЯ и пустая МЕТКА — поломка: их пустыми не бывает", async () => {
    // Личность без имени контроллер не заводит вовсе (`no-name`), а пустая метка сессии
    // означала бы «вошёл», когда входа нет. Одна мерка на все поля разом была бы такой же
    // ошибкой, как и требование непустоты у бренда, — просто в другую сторону.
    for (const порча of [{ name: "" }, { token: "" }]) {
      const { fetch } = отвечает([{ body: JSON.stringify({ ...СВЕЖИЙ_СКОУП, ...порча }) }]);
      const answer = await liveControl(fetch).enter({ addr: СВЕЖИЙ_СКОУП.scope.addr });

      expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
    }
  });

  it("у свежего скоупа пусто ВСЁ, что пульт спрашивает, — и это не отказ", async () => {
    // Свежесозданный скоуп — это личность и пустые списки (`WORLD2` 3.4). Ключей и территорий
    // пульт не спрашивает вовсе, а поля и источники ресурса спрашивает — и пустые их принимает.
    const { fetch } = отвечает([{ body: JSON.stringify({ fields: [] }) }]);
    const поля = await liveControl(fetch).fields();
    const { fetch: fetch2 } = отвечает([{ body: JSON.stringify({ resources: [] }) }]);
    const ресурсы = await liveControl(fetch2).resources();

    expect(поля).toEqual({ kind: "ok", value: [] });
    expect(ресурсы).toEqual({ kind: "ok", value: [] });
  });
});

describe("отказ приходит целиком", () => {
  it("код, причина и выходы контроллера доезжают до человека как есть", async () => {
    const { fetch } = отвечает([{ status: 404, body: JSON.stringify(ОТКАЗ) }]);
    const answer = await liveControl(fetch).enter({ addr: "/scope/егор" });

    // Целиком и слово в слово: причина и ВСЕ выходы контроллера, плюс «сказал это он».
    expect(answer).toEqual({ kind: "refusal", refusal: { ...ОТКАЗ, said: "control" } });
  });

  it("чужой код не переписывается своим и несёт, от кого пришёл", async () => {
    // Контроллер зовёт готовое (`deploy/remote.sh`), и `no-docker` от него — это и есть
    // точная причина. Таблица перевода чужих кодов в свои завела бы второй словарь того же
    // самого: он разъедется с первым на первой же правке соседа.
    const { fetch } = отвечает([
      {
        status: 502,
        body: JSON.stringify({
          code: "no-docker",
          why: "на той машине докера нет",
          ways: ["поставь докер и повтори"],
          from: "deploy/remote.sh",
        }),
      },
    ]);
    const answer = await liveControl(fetch).addResource({ name: "vps", addr: "world@10.8.0.5" });

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("no-docker");
    expect(answer.kind === "refusal" && answer.refusal.from).toBe("deploy/remote.sh");
    expect(answer.kind === "refusal" && answer.refusal.said).toBe("control");
  });

  it("контроллер не отвечает — отказ пульта с адресом и выходом, а не пустой экран", async () => {
    const легла: Fetcher = async () => {
      throw new Error("Failed to fetch");
    };
    const answer = await liveControl(легла).me();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("control-unreachable");
    expect(answer.kind === "refusal" && answer.refusal.said).toBe("panel");
    expect(answer.kind === "refusal" && answer.refusal.ways.join(" ")).toContain("up.sh");
  });

  it("на месте контроллера кто-то другой — сказано, что это не он", async () => {
    const { fetch } = отвечает([{ status: 502, body: "<html>nginx</html>", statusText: "Bad Gateway" }]);
    const answer = await liveControl(fetch).me();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("control-not-answering");
    expect(answer.kind === "refusal" && answer.refusal.why).toContain("502");
  });

  it("вместо ручки приехала статика — отказ говорит именно это", async () => {
    const { fetch } = отвечает([{ body: "<!doctype html><title>пульт</title>" }]);
    const answer = await liveControl(fetch).fields();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-json");
    expect(answer.kind === "refusal" && answer.refusal.why).toContain("не разбирается как JSON");
  });

  it("КАЖДЫЙ отказ, сочинённый пультом, несёт хотя бы один выход", async () => {
    // `WORLD2` 2.3: отказ без выхода — тупик. Проба перебирает все свои отказы разом, чтобы
    // новый нельзя было завести молча без «что делать».
    const свои: Array<Promise<unknown>> = [
      liveControl(async () => {
        throw new Error("сеть");
      }).me(),
      liveControl(отвечает([{ status: 500, body: "" }]).fetch).me(),
      liveControl(отвечает([{ body: "не json" }]).fetch).me(),
      liveControl(отвечает([{ body: "{}" }]).fetch).me(),
      liveControl(отвечает([{ body: "{}" }]).fetch).resources(),
      liveControl(отвечает([{ body: "{}" }]).fetch).fields(),
      liveControl(отвечает([{ body: "{}" }]).fetch).enter({ addr: "/scope/егор" }),
    ];

    for (const обещание of свои) {
      const answer = (await обещание) as { kind: string; refusal: { ways: string[]; said: string } };
      expect(answer.kind).toBe("refusal");
      expect(answer.refusal.said).toBe("panel");
      expect(answer.refusal.ways.length).toBeGreaterThan(0);
      for (const way of answer.refusal.ways) {
        expect(way.trim().length).toBeGreaterThan(0);
      }
    }
  });
});

describe("форма ответа проверяется до показа", () => {
  it("нет массива resources — отказ, а не пустой список", async () => {
    // Пустой список и «ответ не той формы» — разные вещи: показать второе первым значит
    // сказать человеку, что источников нет, когда на деле разъехался контракт.
    const { fetch } = отвечает([{ body: JSON.stringify({ ресурсы: [] }) }]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
    expect(answer.kind === "refusal" && answer.refusal.why).toContain("resources");
  });

  it("строка ресурса без измеренного ответа самого ресурса не показывается вовсе", async () => {
    // `reach` — это ИЗМЕРЕННЫЙ ответ машины. Строка без него врала бы молчанием: человек
    // прочитал бы «ресурс есть» и не узнал бы, что дотянулись ли до него — неизвестно.
    const { fetch } = отвечает([
      { body: JSON.stringify({ resources: [{ name: "vps", addr: "world@10.8.0.5", things: null }] }) },
    ]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
  });

  it("«не спросили» и «спросили, там пусто» — разные ответы, и пульт их не схлопывает", async () => {
    // Молчащий ресурс — не пустая машина (`WORLD2` 4.2). Схлопни пульт `null` в `[]`, и человек
    // прочитал бы «на нём ничего не стоит» там, где на деле до него не дотянулись.
    const { fetch } = отвечает([
      {
        body: JSON.stringify({
          resources: [
            { ...ВТОРОЙ, name: "молчит", things: null },
            { ...ВТОРОЙ, name: "пусто", reach: "отвечает", things: [] },
          ],
        }),
      },
    ]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "ok" && answer.value[0]?.things).toBeNull();
    expect(answer.kind === "ok" && answer.value[1]?.things).toEqual([]);
    expect(answer.kind === "ok" && answer.value[1]?.things).not.toBeNull();
  });

  it("поля «things» нет вовсе — отказ, а не догадка «значит, не спросили»", async () => {
    // «Не спросили» контроллер говорит ВСЛУХ, значением `null`. Молчание контракта его словами
    // подменять нельзя: это уже пульт отвечает за контроллер.
    const { fetch } = отвечает([
      { body: JSON.stringify({ resources: [{ name: "vps", addr: "", reach: "отвечает" }] }) },
    ]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
  });

  it("вещь без HEALTHCHECK-а доезжает как есть — «не подтверждено», а не «здорова»", async () => {
    // У вещи без HEALTHCHECK-а ответа не спросить вовсе, и контроллер говорит это `false` плюс
    // словами. Пульт слова показывает, а `alive` не поправляет: приблизительная запись хуже
    // отсутствующей — она выглядит знанием. То же правило и у соседей (`deploy/remote.sh`).
    const без = { name: "весы", state: "запущена, здоровья не спросить", alive: false };
    const { fetch } = отвечает([
      { body: JSON.stringify({ resources: [{ ...ЗДЕСЬ, things: [без] }] }) },
    ]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "ok" && answer.value[0]?.things).toEqual([без]);
  });

  it("«отвечает ли вещь» спрашивается булевым — не-булево это отказ, а не «наверное, да»", async () => {
    // Мягкая проверка (`!== false`) на любом другом значении выдала бы неспрошенное здоровье за
    // здоровье. Разошлась форма — это вопрос к зоне control, а не догадка в её пользу.
    const { fetch } = отвечает([
      {
        body: JSON.stringify({
          resources: [{ ...ЗДЕСЬ, things: [{ name: "дверь", state: "здорова", alive: "healthy" }] }],
        }),
      },
    ]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
  });

  it("пустой адрес у «здесь» — законная форма, а не поломка", async () => {
    // Адреса у ресурса контроллера нет намеренно: изнутри машины её собственный адрес
    // неизвестен. Требовать его здесь значило бы отказать на правильном ответе.
    const { fetch } = отвечает([{ body: JSON.stringify({ resources: [ЗДЕСЬ] }) }]);
    const answer = await liveControl(fetch).resources();

    expect(answer).toEqual({ kind: "ok", value: [ЗДЕСЬ] });
  });

  it("пустой список полей — законное состояние, а не отказ", async () => {
    const { fetch } = отвечает([{ body: JSON.stringify({ fields: [] }) }]);
    const answer = await liveControl(fetch).fields();

    expect(answer).toEqual({ kind: "ok", value: [] });
  });
});

describe("трасса", () => {
  it("строка пишется на успехе — глагол, путь, код состояния и длительность", async () => {
    const { fetch } = отвечает([{ body: JSON.stringify({ fields: [] }) }]);
    const строки: string[] = [];
    await liveControl(fetch, (line) => строки.push(line)).fields();

    expect(строки).toHaveLength(1);
    expect(строки[0]).toContain(`GET ${PATH.fields}`);
    expect(строки[0]).toContain("http=200");
    expect(строки[0]).toContain("refusal=-");
    expect(строки[0]).toMatch(/dur=\d+(\.\d+)?ms/);
  });

  it("строка пишется и на ОТКАЗЕ, и в ней назван его код", async () => {
    // «Не получилось» без следа в журнале — это разбор по памяти. Поэтому трасса на отказе
    // обязательна, а не «по возможности».
    const { fetch } = отвечает([
      { status: 401, body: JSON.stringify({ code: "not-signed-in", why: "не входили", ways: ["войди"] }) },
    ]);
    const строки: string[] = [];
    await liveControl(fetch, (line) => строки.push(line)).resources();

    expect(строки).toHaveLength(1);
    expect(строки[0]).toContain("http=401");
    expect(строки[0]).toContain("refusal=not-signed-in");
  });

  it("сеть легла — трасса тоже есть, иначе самый частый затык не оставляет следа", async () => {
    const строки: string[] = [];
    await liveControl(
      async () => {
        throw new Error("Failed to fetch");
      },
      (line) => строки.push(line),
    ).me();

    expect(строки).toHaveLength(1);
    expect(строки[0]).toContain("refusal=control-unreachable");
  });
});

describe("добавление", () => {
  it("список источников берётся из того же ответа — второй раз не спрашиваем", async () => {
    // Главное, ради чего человек жал кнопку, — увидеть, что источников стало два
    // (`WORLD2-80`). Лишний запрос между действием и этим видом добавляет только ещё одну
    // возможность не ответить.
    const { fetch, записи } = отвечает([
      { body: JSON.stringify({ resource: ВТОРОЙ, resources: [ЗДЕСЬ, ВТОРОЙ] }), status: 201 },
    ]);
    const answer = await liveControl(fetch).addResource({
      name: "vps",
      addr: "world@10.8.0.5",
      creds: "ключ",
    });

    expect(answer.kind === "ok" && answer.value.resources).toEqual([ЗДЕСЬ, ВТОРОЙ]);
    expect(answer.kind === "ok" && answer.value.added).toEqual(ВТОРОЙ);
    expect(записи).toHaveLength(1);
    expect(записи[0]?.body).toEqual({ name: "vps", addr: "world@10.8.0.5", creds: "ключ" });
  });

  it("слова контроллера о том, что поле НЕ поднято, доезжают до пульта", async () => {
    const note = "поле записано в твой список; само поле пока не поднимается";
    const { fetch } = отвечает([
      {
        status: 201,
        body: JSON.stringify({
          field: { name: "дом", created: "2026-08-14T19:00:00Z" },
          fields: [{ name: "дом", created: "2026-08-14T19:00:00Z" }],
          note,
        }),
      },
    ]);
    const answer = await liveControl(fetch).addField("дом");

    expect(answer.kind === "ok" && answer.value.note).toBe(note);
  });
});
