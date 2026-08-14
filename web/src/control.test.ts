// Пробы разговора с контроллером.
//
// `WORLD2` 4.2: проба, которую ни разу не заставили упасть, пробой не является. Поэтому
// счастливый путь здесь — один тест из многих, а каждый отказ вызывается НАРОЧНО: сеть легла,
// тело не того вида, форма ответа разошлась с контрактом. Проверяем при этом не «покраснело»,
// а что именно сказано человеку: отказ без причины и выходов — провал, а не мелочь
// (`WORLD2` 2.3).
import { describe, expect, it } from "vitest";

import { type Fetcher, PATH, liveControl } from "./control.js";

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

const ВХОД = {
  name: "егор",
  brand: "омнифилд",
  scope: { addr: "/scope/егор", here: true, path: "/scope/егор" },
  created: false,
  token: "метка-1",
};

const ЗДЕСЬ = { name: "here", addr: "", here: true, alive: true, door: "здорова" };
const ВТОРОЙ = {
  name: "vps",
  addr: "world@10.8.0.5",
  here: false,
  alive: true,
  door: "здорова",
};

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
    expect(записи[1]?.headers.Authorization).toBe("Bearer метка-1");
  });
});

describe("отказ приходит целиком", () => {
  it("код, причина и выходы контроллера доезжают до человека как есть", async () => {
    const { fetch } = отвечает([
      {
        status: 404,
        body: JSON.stringify({
          code: "no-scope",
          why: "по адресу /scope/егор скоупа нет — дотянулись, а личности там не лежит",
          ways: ["завести его здесь: POST /api/session с create=true", "или назови другой адрес"],
        }),
      },
    ]);
    const answer = await liveControl(fetch).enter({ addr: "/scope/егор" });

    expect(answer).toEqual({
      kind: "refusal",
      refusal: {
        code: "no-scope",
        why: "по адресу /scope/егор скоупа нет — дотянулись, а личности там не лежит",
        ways: ["завести его здесь: POST /api/session с create=true", "или назови другой адрес"],
        said: "control",
      },
    });
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

  it("строка ресурса без измеренной двери не показывается вовсе", async () => {
    // `door` — это ИЗМЕРЕННОЕ состояние двери. Строка без него врала бы молчанием: человек
    // прочитал бы «ресурс есть» и не узнал бы, что про дверь ничего не известно.
    const { fetch } = отвечает([
      { body: JSON.stringify({ resources: [{ name: "vps", addr: "world@10.8.0.5" }] }) },
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
