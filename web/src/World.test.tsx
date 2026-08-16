// Пробы экрана мира.
//
// Экран показывает три вещи (`WORLD2` 3.3): кто я, мои поля, мои источники ресурса. Пробы
// стерегут не разметку, а обещания: что видно глазами после действия, чего на экране нет
// намеренно и что отказ одного блока не уносит остальные.
import { afterEach, describe, expect, it } from "vitest";

import { World } from "./World.jsx";
import type { Answer, Control, Field, Identity, Resource, Session } from "./control.js";
import { изКонтракта } from "./probe-contract.js";
import { ввести, дождаться, нажать, осесть, смонтировать } from "./probe-dom.jsx";

let проба: { корень: HTMLElement; снять: () => void } | undefined;

afterEach(() => {
  проба?.снять();
  проба = undefined;
});

// Образцы ответов вырезаны из контракта соседа (`probe-contract.ts`), а не написаны рукой:
// экран, проверенный на выдуманном ответе, зеленеет ровно до того дня, когда выдумка перестаёт
// совпадать с контроллером (`WORLD2-131`, где так и вышло с полем про дверь).
// Своего образца у `GET /api/me` в контракте нет — личность там показана ответом входа, её и
// берём. `since` в том образце нет вовсе (у ответа входа его и не бывает), и дата ниже — это
// значение, а не форма: экран показывает её как есть.
const ВОШЁЛ = изКонтракта<Session>("token");
const Я: Identity = {
  name: ВОШЁЛ.name,
  brand: ВОШЁЛ.brand,
  scope: ВОШЁЛ.scope,
  since: "2026-08-14T19:00:00Z",
};

const [ЗДЕСЬ, ВТОРОЙ] = изКонтракта<{ resources: Resource[] }>("resources").resources;

const ok = <T,>(value: T): Answer<T> => ({ kind: "ok", value });

type Ответы = {
  me?: Answer<Identity>;
  resources?: Answer<Resource[]>;
  fields?: Answer<Field[]>;
  addResource?: Answer<{ added: Resource; resources: Resource[] }>;
  addField?: Answer<{ added: Field; fields: Field[]; note: string }>;
};

/** Контроллер для пробы: отвечает тем, что велели, и помнит, чем его позвали. */
function контроллер(ответы: Ответы) {
  const звонки: Array<{ ручка: string; чем: unknown }> = [];
  const control: Control = {
    async enter() {
      throw new Error("экран мира входом не занимается");
    },
    async me() {
      звонки.push({ ручка: "me", чем: undefined });
      return ответы.me ?? ok(Я);
    },
    async resources() {
      звонки.push({ ручка: "resources", чем: undefined });
      return ответы.resources ?? ok([ЗДЕСЬ]);
    },
    async addResource(req) {
      звонки.push({ ручка: "addResource", чем: req });
      return ответы.addResource ?? ok({ added: ВТОРОЙ, resources: [ЗДЕСЬ, ВТОРОЙ] });
    },
    async fields() {
      звонки.push({ ручка: "fields", чем: undefined });
      return ответы.fields ?? ok([]);
    },
    async addField(name) {
      звонки.push({ ручка: "addField", чем: name });
      return (
        ответы.addField ??
        ok({
          added: { name, created: "2026-08-14T19:00:00Z" },
          fields: [{ name, created: "2026-08-14T19:00:00Z" }],
          note: "поле записано в твой список; само поле пока не поднимается",
        })
      );
    },
  };
  return { control, звонки };
}

/** Монтирует экран и ждёт, пока приедут все три блока. */
async function показать(ответы: Ответы = {}) {
  const { control, звонки } = контроллер(ответы);
  проба = смонтировать(() => <World control={control} onLeave={() => {}} />);
  await дождаться(
    () => (проба?.корень.querySelectorAll("[data-state='loading']").length === 0 ? true : null),
    "три блока экрана мира",
  );
  return { корень: проба.корень, звонки };
}

describe("кто я", () => {
  it("показывает имя и бренд — то, что канон называет личностью", async () => {
    const { корень } = await показать();

    expect(корень.querySelector("[data-me-name]")?.textContent).toBe("егор");
    expect(корень.querySelector("[data-me-brand]")?.textContent).toBe("омнифилд");
    expect(корень.textContent).toContain("/scope/егор");
  });

  it("личность перечитывается у контроллера, а не берётся из ответа входа", async () => {
    // Скоуп мог измениться с другой машины: мы обещали связь, а не снимок (`WORLD2` 1.6).
    const { звонки } = await показать();

    expect(звонки.map((з) => з.ручка)).toContain("me");
  });
});

describe("поля", () => {
  it("пустой список — законное состояние, а не отказ", async () => {
    const { корень } = await показать({ fields: ok([]) });
    const блок = корень.querySelector<HTMLElement>("[data-block='fields']")!;

    expect(блок.querySelector("[data-state='empty']")).not.toBeNull();
    expect(блок.querySelector("[data-refusal]")).toBeNull();
    expect(блок.textContent).toContain("законное состояние");
  });

  it("созданное поле появляется в списке, и вслух сказано, что оно НЕ поднято", async () => {
    // Человек, не увидевший этой строки, ждал бы поднятого поля и не получил бы ни его, ни
    // отказа — а это молчание, которое мир себе не позволяет.
    const { корень, звонки } = await показать({ fields: ok([]) });

    ввести(корень, "Создать поле", "дом");
    нажать(корень, "Создать поле");
    await осесть();

    expect(звонки.filter((з) => з.ручка === "addField")).toEqual([{ ручка: "addField", чем: "дом" }]);
    expect(корень.querySelector("[data-field='дом']")).not.toBeNull();
    expect(корень.querySelector("[data-count-fields]")?.textContent).toBe("1");
    expect(корень.textContent).toContain("пока не поднимается");
  });
});

describe("источники ресурса", () => {
  it("«здесь» показан без выдуманного адреса", async () => {
    // Изнутри машины её собственный адрес неизвестен, а подставленный «localhost» стал бы
    // ложью для того, кто смотрит снаружи.
    const { корень } = await показать({ resources: ok([ЗДЕСЬ]) });
    const строка = корень.querySelector<HTMLElement>("[data-resource='here']")!;

    expect(строка.textContent).toContain("здесь стоит контроллер");
    expect(строка.textContent).toContain("изнутри машины не известен");
    expect(строка.textContent).not.toContain("localhost");
  });

  it("про САМ ресурс сказано отдельно от того, что на нём стоит", async () => {
    // Ресурс — машина, до которой дотянулись, а не «машина с дверью» (`WORLD2-131`). Вопросов
    // два, и на экране их тоже два: «отвечает ли машина» и «что на ней поднято».
    const { корень } = await показать({ resources: ok([ЗДЕСЬ]) });
    const строка = корень.querySelector<HTMLElement>(`[data-resource='${ЗДЕСЬ.name}']`)!;

    expect(строка.querySelector("[data-reach]")?.textContent).toBe(ЗДЕСЬ.reach);
    expect(строка.querySelector("[data-things]")?.getAttribute("data-things")).toBe("list");
    expect(строка.querySelector(`[data-thing='${ЗДЕСЬ.things![0]!.name}']`)).not.toBeNull();
  });

  it("«не спросили» и «спросили, там пусто» показаны РАЗНЫМИ ответами", async () => {
    // Молчащий ресурс — не пустая машина. Одна строка на оба случая сказала бы человеку, что на
    // недоступной машине ничего не стоит, — а этого мы не знаем (`WORLD2` 4.2).
    const { корень } = await показать({
      resources: ok([
        { ...ВТОРОЙ, name: "молчит", things: null },
        { ...ВТОРОЙ, name: "пусто", reach: "отвечает", things: [] },
      ]),
    });
    const молчит = корень.querySelector<HTMLElement>("[data-resource='молчит'] [data-things]")!;
    const пусто = корень.querySelector<HTMLElement>("[data-resource='пусто'] [data-things]")!;

    expect(молчит.getAttribute("data-things")).toBe("unknown");
    expect(молчит.textContent).toContain("не спросили");
    expect(пусто.getAttribute("data-things")).toBe("empty");
    expect(пусто.textContent).toContain("ничего не поднято");
    expect(молчит.textContent).not.toBe(пусто.textContent);
  });

  it("вещь без HEALTHCHECK-а не показана здоровой", async () => {
    // Слова состояния — контроллера (`control/internal/resource`, ступени `verdict`), и пульту
    // они непрозрачны: он их показывает, а «отвечает ли» берёт полем `alive`. Выдать
    // неспрошенное здоровье за здоровье — то же самое, что запретили себе соседи
    // (`deploy/remote.sh`: «приблизительная запись хуже отсутствующей»).
    const { корень } = await показать({
      resources: ok([
        {
          ...ЗДЕСЬ,
          things: [{ name: "весы", state: "запущена, здоровья не спросить", alive: false }],
        },
      ]),
    });
    const вещь = корень.querySelector<HTMLElement>("[data-thing='весы']")!;

    expect(вещь.getAttribute("data-alive")).toBe("no");
    expect(вещь.textContent).toContain("здоровья не спросить");
  });

  it("ресурса в цифрах на экране нет: только адрес, сам ресурс и вещи на нём", async () => {
    // `WORLD2` 2.5: память и ядра даёт отдельный инструмент осмотра, и он отложен сознательно.
    // Появится здесь выдуманная цифра — проба покраснеет.
    const { корень } = await показать({ resources: ok([ЗДЕСЬ, ВТОРОЙ]) });
    const строка = корень.querySelector<HTMLElement>(`[data-resource='${ВТОРОЙ.name}']`)!;
    const факты = [...строка.querySelectorAll("dt")].map((dt) => dt.textContent);

    expect(факты).toEqual(["адрес", "ресурс", "вещи"]);
  });

  it("после добавления источников видно два — это главное, что должно быть видно глазами", async () => {
    const { корень, звонки } = await показать({ resources: ok([ЗДЕСЬ]) });
    expect(корень.querySelector("[data-count-resources]")?.textContent).toBe("1");

    ввести(корень, "Имя", "vps");
    ввести(корень, "Адрес", "world@10.8.0.5");
    ввести(корень, "Креды", "ключ целиком");
    нажать(корень, "Добавить ресурс");
    await осесть();

    expect(звонки.filter((з) => з.ручка === "addResource")).toEqual([
      { ручка: "addResource", чем: { name: "vps", addr: "world@10.8.0.5", creds: "ключ целиком" } },
    ]);
    expect(корень.querySelector("[data-count-resources]")?.textContent).toBe("2");
    expect(корень.querySelector("[data-resource='vps']")).not.toBeNull();
    // Список берётся из ответа на добавление — второй раз контроллер не спрашивается.
    expect(звонки.filter((з) => з.ручка === "resources")).toHaveLength(1);
  });

  it("отказ добавления показан рядом с формой, а экран мира остаётся на месте", async () => {
    const { корень } = await показать({
      resources: ok([ЗДЕСЬ]),
      addResource: {
        kind: "refusal",
        refusal: {
          code: "no-docker",
          why: "на той машине докера нет",
          ways: ["поставь докер и повтори"],
          said: "control",
          from: "deploy/remote.sh",
        },
      },
    });

    ввести(корень, "Имя", "vps");
    ввести(корень, "Адрес", "world@10.8.0.5");
    нажать(корень, "Добавить ресурс");
    const блок = await дождаться(
      () => корень.querySelector<HTMLElement>("[data-refusal='no-docker']"),
      "отказ добавления",
    );

    // Чужой код оставлен как есть и назван вместе с тем, от кого пришёл.
    expect(блок.textContent).toContain("deploy/remote.sh");
    // Экран не пропал: имя и список источников на месте.
    expect(корень.querySelector("[data-me-name]")?.textContent).toBe("егор");
    expect(корень.querySelector("[data-resource='here']")).not.toBeNull();
  });
});

describe("отказ одного блока не уносит остальные", () => {
  it("ресурсы отказали — имя и поля видны, у отказа есть выход", async () => {
    const { корень } = await показать({
      resources: {
        kind: "refusal",
        refusal: {
          code: "no-daemon",
          why: "докер на этой машине не отвечает, а список ресурсов — это его контексты",
          ways: ["проверь, что сокет докера отдан контроллеру при подъёме"],
          said: "control",
        },
      },
    });

    expect(корень.querySelector("[data-block='resources'] [data-refusal]")).not.toBeNull();
    expect(корень.querySelector("[data-me-name]")?.textContent).toBe("егор");
    expect(корень.querySelector("[data-block='fields'] [data-state='empty']")).not.toBeNull();
    expect(корень.textContent).toContain("сокет докера");
  });
});
