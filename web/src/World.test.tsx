// Пробы экрана мира.
//
// Экран показывает три вещи (`WORLD2` 3.3): кто я, мои поля, мои источники ресурса. Пробы
// стерегут не разметку, а обещания: что видно глазами после действия, чего на экране нет
// намеренно и что отказ одного блока не уносит остальные.
import { afterEach, describe, expect, it } from "vitest";

import { World } from "./World.jsx";
import type { Answer, Control, Field, Identity, Resource } from "./control.js";
import { ввести, дождаться, нажать, осесть, смонтировать } from "./probe-dom.jsx";

let проба: { корень: HTMLElement; снять: () => void } | undefined;

afterEach(() => {
  проба?.снять();
  проба = undefined;
});

const Я: Identity = {
  name: "егор",
  brand: "омнифилд",
  scope: { addr: "/scope/егор", here: true, path: "/scope/егор" },
  since: "2026-08-14T19:00:00Z",
};

const ЗДЕСЬ: Resource = { name: "here", addr: "", here: true, alive: true, door: "здорова" };
const ВТОРОЙ: Resource = {
  name: "vps",
  addr: "world@10.8.0.5",
  here: false,
  alive: true,
  door: "здорова",
};

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

  it("жив ли — сказано словами про ДВЕРЬ, а не про докер", async () => {
    const { корень } = await показать({
      resources: ok([{ ...ВТОРОЙ, alive: false, door: "ресурс молчит" }]),
    });
    const строка = корень.querySelector<HTMLElement>("[data-resource='vps']")!;

    expect(строка.querySelector("[data-door]")?.textContent).toContain("ресурс молчит");
    expect(строка.querySelector("[data-door]")?.getAttribute("data-door")).toBe("dead");
  });

  it("ресурса в цифрах на экране нет: только имя, адрес и дверь", async () => {
    // `WORLD2` 2.5: память и ядра даёт отдельный инструмент осмотра, и он отложен сознательно.
    // Появится здесь выдуманная цифра — проба покраснеет.
    const { корень } = await показать({ resources: ok([ЗДЕСЬ, ВТОРОЙ]) });
    const строка = корень.querySelector<HTMLElement>("[data-resource='vps']")!;
    const факты = [...строка.querySelectorAll("dt")].map((dt) => dt.textContent);

    expect(факты).toEqual(["адрес", "дверь"]);
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
