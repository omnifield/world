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

import {
  type Fetcher,
  PATH,
  type Resource,
  type Session,
  liveControl,
} from "./control.js";
import { изКонтракта, свежийСкоуп } from "./probe-contract.js";

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

/** Адрес и пароль скоупа — то единственное, чем входят (`WORLD2` 3.7). */
const АДРЕС = ВХОД.scope.addr;
const ПАРОЛЬ = "пароль-скоупа";

/**
 * Территории — образец оттуда же. В нём УЖЕ есть оба ответа про вещи: у одной машины список, у
 * молчащей `null`. Это не совпадение, а то самое место контракта, ради которого он и написан
 * так: «не спросили» и «пусто» — разные ответы.
 *
 * Ресурса «здесь» в образце нет и быть не может (`WORLD2-132`): список приезжает из скоупа, а
 * машина контроллера в личность юзера не входит.
 */
const [УЧАСТОК, МОЛЧУН] = изКонтракта<{ resources: Resource[] }>("resources").resources;

/** Отказ контроллера — образец оттуда же («Отказ — код, причина, выход»). */
const ОТКАЗ = изКонтракта<{ code: string; why: string; ways: string[] }>("code");

/** Ответ выхода — форма из контроллера: сказано, что вышли, и сказано, чего он НЕ трогал. */
const ВЫХОД = {
  out: true,
  note: "вышли: контексты докера, ключи и блоки в config сняты. Скоуп не тронут",
};

/**
 * Креды к машине — ДВА ВИДА, и вид называется явно (`WORLD2-141`). Плоской строкой они
 * больше не бывают: контроллер такую не принимает вовсе.
 */
const КЛЮЧ = { kind: "key", value: "ключ целиком" } as const;
const ПАРОЛЬ_МАШИНЫ = { kind: "password", value: "пароль машины" } as const;

/**
 * Цена, названная контроллером ПОСЛЕ записи: он изменил чужую машину. Приезжает только на
 * пути с паролем — заведение по ключу не меняет на той стороне ничего.
 */
const ЦЕНА =
  "на машину world@10.8.0.5 положен публичный ключ юзера (одна строка в её " +
  "~/.ssh/authorized_keys, подпись world-control) — пароль дальше не нужен и нигде не сохранён";

describe("вход — адрес и пароль, и больше ничего", () => {
  it("отдаёт личность и скоуп в том виде, в каком их назвал контроллер", async () => {
    const { fetch, записи } = отвечает([{ body: JSON.stringify(ВХОД) }]);
    const answer = await liveControl(fetch).enter({ addr: АДРЕС, password: ПАРОЛЬ });

    // `since` и `note` пульт ставит пустыми: первого у ответа входа не бывает вовсе, второе
    // приезжает только тогда, когда контроллер и правда изменил чужую машину.
    expect(answer).toEqual({ kind: "ok", value: { ...ВХОД, since: "", note: "" } });
    expect(записи[0]?.path).toBe(PATH.session);
    expect(записи[0]?.method).toBe("POST");
  });

  it("в теле входа ТОЛЬКО адрес и пароль: хода «завести здесь» не существует", async () => {
    // `WORLD2` 3.7, решение user 2026-08-16. Разница между «состояние есть» и «состояния нет»
    // только в исходе, а спрашивается одно и то же. Вернётся сюда `create` — контроллер
    // откажет `bad-body`, но пульт обязан не посылать его и подавно: молча проглоченное поле
    // выглядит сработавшим. Проба стережёт СОСТАВ тела, а не наличие нужных полей.
    const { fetch, записи } = отвечает([{ body: JSON.stringify(ВХОД) }]);
    await liveControl(fetch).enter({ addr: АДРЕС, password: ПАРОЛЬ });

    expect(записи[0]?.body).toEqual({ addr: АДРЕС, password: ПАРОЛЬ });
  });

  it("метка сессии едет заголовком в следующие запросы, а не только печеньем", async () => {
    // Печенье контроллер ставит сам, но полагаться только на него нельзя: пульт может
    // раздаваться не с того origin, где стоит контроллер, — этот шов ещё не решён.
    const { fetch, записи } = отвечает([
      { body: JSON.stringify(ВХОД) },
      { body: JSON.stringify({ resources: [УЧАСТОК] }) },
    ]);
    const control = liveControl(fetch);
    await control.enter({ addr: АДРЕС, password: ПАРОЛЬ });
    await control.resources();

    expect(записи[0]?.headers.Authorization).toBeUndefined();
    expect(записи[1]?.headers.Authorization).toBe(`Bearer ${ВХОД.token}`);
  });
});

describe("завести скоуп — там, где он будет лежать", () => {
  it("уходит СВОЕЙ ручкой, а не полем входа", async () => {
    // Заведение и вход — разные разговоры (`WORLD2` 3.4, 3.7), и разведены они путями, а не
    // флагом в одном теле: флаг однажды заводит личность на опечатке в адресе.
    const { fetch, записи } = отвечает([
      { body: JSON.stringify({ ...ВХОД, created: true }), status: 201 },
    ]);
    const answer = await liveControl(fetch).createScope({
      scope: { addr: АДРЕС, password: ПАРОЛЬ },
      identity: { name: "егор", brand: "омнифилд" },
      machine: { name: "vps", addr: "world@10.8.0.5", creds: КЛЮЧ },
    });

    expect(записи[0]?.path).toBe(PATH.scope);
    expect(записи[0]?.method).toBe("POST");
    expect(answer.kind === "ok" && answer.value.created).toBe(true);
  });

  it("две пары уезжают РАЗДЕЛЬНО — слить их в один набор полей не выйдет", async () => {
    // Машина — куда дотянуться и что поднять; скоуп — по какому адресу входить. На слиянии
    // этих двух пар выросла мёртвая `WORLD2-77` (`WORLD2` 3.4, «Два адреса»).
    const { fetch, записи } = отвечает([
      { body: JSON.stringify({ ...ВХОД, created: true }), status: 201 },
    ]);
    await liveControl(fetch).createScope({
      scope: { addr: АДРЕС, password: ПАРОЛЬ },
      identity: { name: "егор", brand: "омнифилд" },
      machine: { name: "vps", addr: "world@10.8.0.5", creds: КЛЮЧ },
    });

    expect(записи[0]?.body).toEqual({
      scope: { addr: АДРЕС, password: ПАРОЛЬ },
      identity: { name: "егор", brand: "омнифилд" },
      machine: { name: "vps", addr: "world@10.8.0.5", creds: КЛЮЧ },
    });
  });

  it("раздачу юзер поднял сам — машины в теле нет ВОВСЕ, а не пустым объектом", async () => {
    // Чужая вилка равноправна (`WORLD2` 0.3): раздачи может не быть нашей, и мир в неё не
    // смотрит. Контроллер отличает «машину не назвали» от «назвали пустую» наличием ключа —
    // пустой объект он разобрал бы как названную машину без имени.
    const { fetch, записи } = отвечает([
      { body: JSON.stringify({ ...ВХОД, created: true }), status: 201 },
    ]);
    await liveControl(fetch).createScope({
      scope: { addr: АДРЕС, password: ПАРОЛЬ },
      identity: { name: "егор", brand: "омнифилд" },
    });

    expect(записи[0]?.body).toEqual({
      scope: { addr: АДРЕС, password: ПАРОЛЬ },
      identity: { name: "егор", brand: "омнифилд" },
    });
    expect(Object.keys(записи[0]?.body as object)).not.toContain("machine");
  });

  it("метка заведения запоминается так же, как метка входа", async () => {
    const { fetch, записи } = отвечает([
      { body: JSON.stringify({ ...ВХОД, created: true }), status: 201 },
      { body: JSON.stringify({ fields: [] }) },
    ]);
    const control = liveControl(fetch);
    await control.createScope({
      scope: { addr: АДРЕС, password: ПАРОЛЬ },
      identity: { name: "егор", brand: "" },
    });
    await control.fields();

    expect(записи[1]?.headers.Authorization).toBe(`Bearer ${ВХОД.token}`);
  });
});

describe("выход — ручка, а не забытая метка", () => {
  it("уходит глаголом DELETE по адресу сессии", async () => {
    // Выход снимает ВРЕМЯНКИ КОНТРОЛЛЕРА — контексты, ключи, блоки в config. Забыть метку у
    // себя значило бы оставить их лежать: следующий вошедший увидел бы чужие территории, и
    // «личность» перестала бы что-то значить (`WORLD2-132`).
    const { fetch, записи } = отвечает([
      { body: JSON.stringify(ВХОД) },
      { body: JSON.stringify(ВЫХОД) },
    ]);
    const control = liveControl(fetch);
    await control.enter({ addr: АДРЕС, password: ПАРОЛЬ });
    const answer = await control.leave();

    expect(записи[1]?.path).toBe(PATH.session);
    expect(записи[1]?.method).toBe("DELETE");
    expect(записи[1]?.body).toBeUndefined();
    expect(answer.kind === "ok" && answer.value.note).toBe(ВЫХОД.note);
  });

  it("после выхода метка не едет никуда — сессии больше нет", async () => {
    const { fetch, записи } = отвечает([
      { body: JSON.stringify(ВХОД) },
      { body: JSON.stringify(ВЫХОД) },
      { body: JSON.stringify({ resources: [] }) },
    ]);
    const control = liveControl(fetch);
    await control.enter({ addr: АДРЕС, password: ПАРОЛЬ });
    await control.leave();
    await control.resources();

    expect(записи[2]?.headers.Authorization).toBeUndefined();
  });

  it("выход ОТКАЗАЛ — метка остаётся: несостоявшийся выход не выдаётся за состоявшийся", async () => {
    // Уронив метку на отказе, пульт сделал бы сессию недостижимой: времянки не сняты, а
    // дотянуться до них уже нечем. Это ровно тот молчаливый разъезд, которого ступень 2 и
    // избегает.
    const { fetch, записи } = отвечает([
      { body: JSON.stringify(ВХОД) },
      { status: 502, body: JSON.stringify({ code: "no-daemon", why: "докер не отвечает", ways: ["подними демон"] }) },
      { body: JSON.stringify({ resources: [] }) },
    ]);
    const control = liveControl(fetch);
    await control.enter({ addr: АДРЕС, password: ПАРОЛЬ });
    const выход = await control.leave();
    await control.resources();

    expect(выход.kind === "refusal" && выход.refusal.code).toBe("no-daemon");
    expect(записи[2]?.headers.Authorization).toBe(`Bearer ${ВХОД.token}`);
  });

  it("контроллер не сказал «вышли» — это отказ, а не догадка в пользу выхода", async () => {
    // `out` у ответа выхода — не украшение: им контроллер и говорит, что времянки сняты.
    const { fetch } = отвечает([{ body: JSON.stringify({ note: "что-то сделали" }) }]);
    const answer = await liveControl(fetch).leave();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
    expect(answer.kind === "refusal" && answer.refusal.why).toContain("out");
  });
});

describe("креды к машине — вид называется явно, и цена доезжает словами", () => {
  it("вид уезжает ОБЪЕКТОМ, а не плоской строкой", async () => {
    // `WORLD2-141`: угадывать вид по виду строки нельзя — угаданный однажды примет ключ за
    // пароль, и разбираться человек будет с отказом ssh, а не с нашей догадкой. Плоскую
    // строку контроллер не принимает вовсе.
    const { fetch, записи } = отвечает([
      { body: JSON.stringify({ resource: УЧАСТОК, resources: [УЧАСТОК] }), status: 201 },
    ]);
    await liveControl(fetch).addResource({
      name: "vps",
      addr: "world@10.8.0.5",
      creds: ПАРОЛЬ_МАШИНЫ,
    });

    expect((записи[0]?.body as { creds: unknown }).creds).toEqual({
      kind: "password",
      value: "пароль машины",
    });
  });

  it("цена, названная контроллером, доезжает до человека дословно — и при заведении тоже", async () => {
    // Контроллер изменил ЧУЖУЮ машину и говорит об этом словами. Проглотить их значило бы
    // смолчать о том, что мир тронул машину юзера.
    const { fetch } = отвечает([
      { body: JSON.stringify({ resource: УЧАСТОК, resources: [УЧАСТОК], note: ЦЕНА }), status: 201 },
    ]);
    const добавили = await liveControl(fetch).addResource({
      name: "vps",
      addr: "world@10.8.0.5",
      creds: ПАРОЛЬ_МАШИНЫ,
    });

    const { fetch: fetch2 } = отвечает([
      { body: JSON.stringify({ ...ВХОД, created: true, note: ЦЕНА }), status: 201 },
    ]);
    const завели = await liveControl(fetch2).createScope({
      scope: { addr: АДРЕС, password: ПАРОЛЬ },
      identity: { name: "егор", brand: "" },
      machine: { name: "vps", addr: "world@10.8.0.5", creds: ПАРОЛЬ_МАШИНЫ },
    });

    expect(добавили.kind === "ok" && добавили.value.note).toBe(ЦЕНА);
    expect(завели.kind === "ok" && завели.value.note).toBe(ЦЕНА);
  });

  it("цены НЕТ — это не поломка формы: путь по ключу чужую машину не трогает", async () => {
    // `note` приезжает только тогда, когда есть что сказать. Требовать его всегда значило бы
    // отказать на правильном ответе мира — той же ошибкой, что стоила входа новичкам.
    const { fetch } = отвечает([
      { body: JSON.stringify({ resource: УЧАСТОК, resources: [УЧАСТОК] }), status: 201 },
    ]);
    const answer = await liveControl(fetch).addResource({
      name: "vps",
      addr: "world@10.8.0.5",
      creds: КЛЮЧ,
    });

    expect(answer.kind).toBe("ok");
    expect(answer.kind === "ok" && answer.value.note).toBe("");
  });
});

describe("свежесозданный скоуп — первое, что видит новый юзер", () => {
  // `WORLD2-135`: пульт требовал непустой бренд и не пускал в мир НИКОГО, кто входит впервые.
  // Ни одна из проб зоны этого не поймала, и сверка с образцом контракта не поймала бы тоже:
  // пример в доке показывает форму, а не законный край значений — бренд там назван.
  const СВЕЖИЙ = свежийСкоуп<Omit<Session, "since">>();

  it("заведение с ПУСТЫМ брендом проходит: пустое законно, а не поломка ответа", async () => {
    const { fetch } = отвечает([{ body: JSON.stringify(СВЕЖИЙ), status: 201 }]);
    const answer = await liveControl(fetch).createScope({
      scope: { addr: АДРЕС, password: ПАРОЛЬ },
      identity: { name: СВЕЖИЙ.name, brand: "" },
    });

    expect(answer.kind).toBe("ok");
    expect(answer.kind === "ok" && answer.value.brand).toBe("");
    expect(answer.kind === "ok" && answer.value.created).toBe(true);
  });

  it("пустой бренд ничем не подменяется — ни именем, ни прочерком", async () => {
    // Мир не выдумывает за юзера (`WORLD2` 3.7, 0.1): пустое доезжает пустым, а называется
    // словами уже на экране.
    const { fetch } = отвечает([{ body: JSON.stringify(СВЕЖИЙ) }]);
    const answer = await liveControl(fetch).me();

    expect(answer.kind === "ok" && answer.value.brand).toBe("");
  });

  it("а вот бренда НЕ СТРОКОЙ — это уже форма, и она отказ", async () => {
    // «Поле пустое» и «поля нет» — разные вещи. Первое законно, второе значит, что контракт
    // разъехался, и разбирать такой ответ дальше нельзя.
    const { brand: _, ...без } = СВЕЖИЙ;
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
      const { fetch } = отвечает([{ body: JSON.stringify({ ...СВЕЖИЙ, ...порча }) }]);
      const answer = await liveControl(fetch).enter({ addr: АДРЕС, password: ПАРОЛЬ });

      expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
    }
  });

  it("у свежего скоупа пусто ВСЁ, что пульт спрашивает, — и это не отказ", async () => {
    // Свежесозданный скоуп — это личность и пустые списки (`WORLD2` 3.4). Ключей пульт не
    // спрашивает вовсе, а поля и территории спрашивает — и пустые их принимает. Со ступени 2
    // пустой список территорий стал обычным состоянием: ресурса «здесь» в нём больше нет.
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
    const answer = await liveControl(fetch).enter({ addr: АДРЕС, password: ПАРОЛЬ });

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
    const answer = await liveControl(fetch).addResource({
      name: "vps",
      addr: "world@10.8.0.5",
      creds: КЛЮЧ,
    });

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
      liveControl(отвечает([{ body: "{}" }]).fetch).leave(),
      liveControl(отвечает([{ body: "{}" }]).fetch).enter({ addr: АДРЕС, password: ПАРОЛЬ }),
      liveControl(отвечает([{ body: "{}" }]).fetch).createScope({
        scope: { addr: АДРЕС, password: ПАРОЛЬ },
        identity: { name: "егор", brand: "" },
      }),
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
  it("скоуп без машины, на которой он раздаётся, — отказ", async () => {
    // `scope` сменил состав вместе со ступенью 2: было «лежит ли он здесь» и «путь на
    // машине», стало «адрес раздачи» и «машина». Пара `here`/`path` и означала личность,
    // лежащую томом контроллера (`WORLD2-124`), — вернётся она сюда, и проба покраснеет.
    const { fetch } = отвечает([
      { body: JSON.stringify({ ...ВХОД, scope: { addr: АДРЕС, here: true, path: "/scope/егор" } }) },
    ]);
    const answer = await liveControl(fetch).enter({ addr: АДРЕС, password: ПАРОЛЬ });

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
    expect(answer.kind === "refusal" && answer.refusal.why).toContain("host");
  });

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

  it("участок без адреса — тоже отказ: безадресного «здесь» больше не бывает", async () => {
    // До ступени 2 пустой адрес был законной формой — так выглядела машина контроллера,
    // ресурс «здесь». Теперь список берётся из СКОУПА, каждый участок юзер завёл по адресу,
    // который назвал сам, и пустой адрес значит разъехавшийся контракт (`WORLD2-132`).
    const { fetch } = отвечает([
      { body: JSON.stringify({ resources: [{ ...УЧАСТОК, addr: "" }] }) },
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
            { ...МОЛЧУН, name: "молчит", things: null },
            { ...МОЛЧУН, name: "пусто", reach: "отвечает", things: [] },
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
      { body: JSON.stringify({ resources: [{ name: "vps", addr: "world@10.8.0.5", reach: "отвечает" }] }) },
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
      { body: JSON.stringify({ resources: [{ ...УЧАСТОК, things: [без] }] }) },
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
          resources: [{ ...УЧАСТОК, things: [{ name: "дверь", state: "здорова", alive: "healthy" }] }],
        }),
      },
    ]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
  });

  it("комьюнити участка доезжают списком — и пустой список законен", async () => {
    // Форма соседа поехала (`WORLD2-122`): у участка появились комьюнити, в которых он состоит.
    // Их сколько угодно (`WORLD2` 2.5 п. 3), а пустой список — обычное состояние, а не «не
    // спросили»: принадлежность лежит в скоупе, и спрашивать про неё некого.
    const { fetch } = отвечает([{ body: JSON.stringify({ resources: [УЧАСТОК, МОЛЧУН] }) }]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "ok" && answer.value[0]?.fields).toEqual(УЧАСТОК.fields);
    expect(УЧАСТОК.fields.length).toBeGreaterThan(0);
    expect(answer.kind === "ok" && answer.value[1]?.fields).toEqual([]);
  });

  it("поля «fields» нет вовсе — отказ: комьюнити не догадка пульта", async () => {
    // Контроллер пишет список ВСЕГДА и `null` вместо пустого не отдаёт — нормализует у себя.
    // Подставить сюда `[]` за него значило бы сказать «ни в одном» там, где мы не знаем
    // ничего: то же самое, что запрещено у вещей.
    const { fields: _, ...без } = УЧАСТОК;
    const { fetch } = отвечает([{ body: JSON.stringify({ resources: [без] }) }]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
  });

  it("`fields: null` — тоже отказ: у принадлежности нет «не спросили»", async () => {
    // У ВЕЩЕЙ `null` законен, у комьюнити — нет, и мерка эта взята оттуда, где поле рождается.
    // Вещи спрашивают у машины, и она может молчать; комьюнити лежат в скоупе, а он перед нами.
    const { fetch } = отвечает([
      { body: JSON.stringify({ resources: [{ ...УЧАСТОК, fields: null }] }) },
    ]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
  });

  it("безымянное комьюнити в списке — отказ, а не строка без имени", async () => {
    // За именем комьюнити стоит адрес, как и за именем ключа (`WORLD2` 3.4 п. 4): пустым оно
    // не приезжает, и показанная пустая строка списка означала бы членство неизвестно в чём.
    const { fetch } = отвечает([
      { body: JSON.stringify({ resources: [{ ...УЧАСТОК, fields: ["дом", ""] }] }) },
    ]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
  });

  it("заявленный ресурс доезжает словами юзера, и пустой — законное значение", async () => {
    // `resource` — что участок ЗАЯВИЛ о себе сам (`WORLD2` 2.5 пп. 2, 6, 7), а не мера мира.
    // Пусто значит «не заявлял», и мерка тут та же, что у бренда: пустая строка законна.
    const { fetch } = отвечает([{ body: JSON.stringify({ resources: [УЧАСТОК, МОЛЧУН] }) }]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "ok" && answer.value[0]?.resource).toBe(УЧАСТОК.resource);
    expect(УЧАСТОК.resource).not.toBe("");
    expect(answer.kind === "ok" && answer.value[1]?.resource).toBe("");
  });

  it("поля «resource» нет вовсе — отказ: «не заявлял» говорит контроллер, а не пульт", async () => {
    // Пустая строка и отсутствие поля — разные ответы (`WORLD2` 3.4, `WORLD2-135`). Первое
    // сказал контроллер, второе значит, что форма разъехалась.
    const { resource: _, ...без } = УЧАСТОК;
    const { fetch } = отвечает([{ body: JSON.stringify({ resources: [без] }) }]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
  });

  it("заявленный ресурс не строкой — отказ, а не «наверное, цифры»", async () => {
    // Заявление это СЛОВА участка, и цифр в нём может не быть вовсе. Приехало число — форма
    // разошлась, и это вопрос к зоне control, а не повод показать его как есть.
    const { fetch } = отвечает([
      { body: JSON.stringify({ resources: [{ ...УЧАСТОК, resource: 4 }] }) },
    ]);
    const answer = await liveControl(fetch).resources();

    expect(answer.kind === "refusal" && answer.refusal.code).toBe("answer-not-expected");
  });

  it("образец контракта разбирается целиком — форма соседа и мерки пульта сходятся", async () => {
    const { fetch } = отвечает([{ body: JSON.stringify({ resources: [УЧАСТОК, МОЛЧУН] }) }]);
    const answer = await liveControl(fetch).resources();

    expect(answer).toEqual({ kind: "ok", value: [УЧАСТОК, МОЛЧУН] });
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

  it("глагол в трассе НАСТОЯЩИЙ — выход виден выходом, а не запросом без тела", async () => {
    // Трасса читается рядом с трассой контроллера одним взглядом. Выведи мы глагол из наличия
    // тела, `DELETE /api/session` встал бы в журнал как `GET`, и две трассы разошлись бы ровно
    // на том действии, которое чаще всего и разбирают.
    const { fetch } = отвечает([{ body: JSON.stringify(ВЫХОД) }]);
    const строки: string[] = [];
    await liveControl(fetch, (line) => строки.push(line)).leave();

    expect(строки[0]).toContain(`DELETE ${PATH.session}`);
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
      { body: JSON.stringify({ resource: МОЛЧУН, resources: [УЧАСТОК, МОЛЧУН] }), status: 201 },
    ]);
    const answer = await liveControl(fetch).addResource({
      name: "home",
      addr: "world@10.8.0.6",
      creds: КЛЮЧ,
    });

    expect(answer.kind === "ok" && answer.value.resources).toEqual([УЧАСТОК, МОЛЧУН]);
    expect(answer.kind === "ok" && answer.value.added).toEqual(МОЛЧУН);
    expect(записи).toHaveLength(1);
    expect(записи[0]?.body).toEqual({ name: "home", addr: "world@10.8.0.6", creds: КЛЮЧ });
  });

  it("слова контроллера о том, что поле НЕ поднято, доезжают до пульта", async () => {
    const note = "поле записано в твой скоуп; само поле пока не поднимается";
    const { fetch } = отвечает([
      {
        status: 201,
        body: JSON.stringify({
          field: { name: "дом" },
          fields: [{ name: "дом", addr: "", state: "" }],
          note,
        }),
      },
    ]);
    const answer = await liveControl(fetch).addField("дом");

    expect(answer.kind === "ok" && answer.value.note).toBe(note);
  });
});
