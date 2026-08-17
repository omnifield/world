// Пробы экрана мира.
//
// Экран показывает три вещи (`WORLD2` 3.3): кто я, мои поля, мои источники ресурса. Пробы
// стерегут не разметку, а обещания: что видно глазами после действия, чего на экране нет
// намеренно и что отказ одного блока не уносит остальные.
import { afterEach, describe, expect, it } from "vitest";

import { World } from "./World.jsx";
import type {
  Answer,
  Control,
  Field,
  Identity,
  Leaving,
  Refusal,
  Resource,
  Session,
} from "./control.js";
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

// Территории юзера — из его скоупа. Ресурса «здесь» среди них нет и быть не может
// (`WORLD2-132`): машина контроллера юзеру не принадлежит.
const [УЧАСТОК, МОЛЧУН] = изКонтракта<{ resources: Resource[] }>("resources").resources;

const ok = <T,>(value: T): Answer<T> => ({ kind: "ok", value });

const ПРОЩАНИЕ = "вышли: контексты докера, ключи и блоки в config сняты. Скоуп не тронут";

type Ответы = {
  me?: Answer<Identity>;
  resources?: Answer<Resource[]>;
  fields?: Answer<Field[]>;
  addResource?: Answer<{ added: Resource; resources: Resource[]; note: string }>;
  addField?: Answer<{ added: Field; fields: Field[]; note: string }>;
  leave?: Answer<Leaving>;
};

/** Контроллер для пробы: отвечает тем, что велели, и помнит, чем его позвали. */
function контроллер(ответы: Ответы) {
  const звонки: Array<{ ручка: string; чем: unknown }> = [];
  const control: Control = {
    async enter() {
      throw new Error("экран мира входом не занимается");
    },
    async createScope() {
      throw new Error("экран мира заведением не занимается");
    },
    async leave() {
      звонки.push({ ручка: "leave", чем: undefined });
      return ответы.leave ?? ok({ note: ПРОЩАНИЕ });
    },
    async me() {
      звонки.push({ ручка: "me", чем: undefined });
      return ответы.me ?? ok(Я);
    },
    async resources() {
      звонки.push({ ручка: "resources", чем: undefined });
      return ответы.resources ?? ok([УЧАСТОК]);
    },
    async addResource(req) {
      звонки.push({ ручка: "addResource", чем: req });
      return ответы.addResource ?? ok({ added: МОЛЧУН, resources: [УЧАСТОК, МОЛЧУН], note: "" });
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
          added: { name, addr: "", state: "" },
          fields: [{ name, addr: "", state: "" }],
          note: "поле записано в твой скоуп; само поле пока не поднимается",
        })
      );
    },
  };
  return { control, звонки };
}

/** Монтирует экран и ждёт, пока приедут все три блока. */
async function показать(ответы: Ответы = {}, onLeave: (сказано?: string) => void = () => {}) {
  const { control, звонки } = контроллер(ответы);
  проба = смонтировать(() => <World control={control} onLeave={onLeave} />);
  await дождаться(
    () => (проба?.корень.querySelectorAll("[data-state='loading']").length === 0 ? true : null),
    "три блока экрана мира",
  );
  return { корень: проба.корень, звонки };
}

describe("кто я", () => {
  it("показывает имя и бренд — то, что канон называет личностью", async () => {
    const { корень } = await показать();

    expect(корень.querySelector("[data-me-name]")?.textContent).toBe(Я.name);
    expect(корень.querySelector("[data-me-brand]")?.textContent).toBe(Я.brand);
  });

  it("скоуп показан АДРЕСОМ и машиной, на которой он раздаётся", async () => {
    // `WORLD2-132`: личность лежит по адресу и раздаётся оттуда. Прежней приписки «на ресурсе
    // контроллера» больше нет — вопрос «на этом ли он ресурсе» исчез вместе с ответом «да».
    const { корень } = await показать();
    const скоуп = корень.querySelector<HTMLElement>("[data-me-scope]")!;

    expect(скоуп.textContent).toContain(Я.scope.addr);
    expect(корень.querySelector("[data-scope-host]")?.textContent).toBe(Я.scope.host);
    expect(скоуп.textContent).not.toContain("на ресурсе контроллера");
  });

  it("пустой бренд показан СЛОВАМИ, а не пустым местом", async () => {
    // `WORLD2-135`: бренда у свежей личности нет, и это законное состояние. Пустая ячейка
    // заставила бы человека гадать, отвалилось ли что-то.
    const { корень } = await показать({ me: ok({ ...Я, brand: "" }) });
    const бренд = корень.querySelector<HTMLElement>("[data-me-brand]")!;

    expect(бренд.querySelector("[data-state='empty']")).not.toBeNull();
    expect(бренд.textContent).toContain("бренда пока нет");
  });

  it("пустой бренд ничем не подменяется — ни именем, ни прочерком", async () => {
    // Мир не выдумывает за юзера (`WORLD2` 3.7, 0.1). Появится здесь имя вместо бренда —
    // человек прочитает как свой бренд то, чего он не называл.
    const { корень } = await показать({ me: ok({ ...Я, brand: "" }) });
    const бренд = корень.querySelector<HTMLElement>("[data-me-brand]")!;

    expect(бренд.textContent).not.toContain(Я.name);
    expect(бренд.textContent?.trim()).not.toBe("—");
  });

  it("личность перечитывается у контроллера, а не берётся из ответа входа", async () => {
    // Скоуп мог измениться с другой машины: мы обещали связь, а не снимок (`WORLD2` 1.6).
    const { звонки } = await показать();

    expect(звонки.map((з) => з.ручка)).toContain("me");
  });
});

describe("выход", () => {
  it("зовёт РУЧКУ выхода, а не забывает метку у себя", async () => {
    // Выход снимает времянки контроллера — контексты, ключи, блоки в config (`WORLD2-132`).
    // Забытая метка оставила бы их лежать, и следующий вошедший увидел бы чужие территории.
    let сказано: string | undefined;
    const { корень, звонки } = await показать({}, (слова) => (сказано = слова));

    нажать(корень, "Выйти");
    await осесть();

    expect(звонки.filter((з) => з.ручка === "leave")).toHaveLength(1);
    expect(сказано).toBe(ПРОЩАНИЕ);
  });

  it("выход ОТКАЗАЛ — остаёмся на экране мира, а причина названа", async () => {
    // Уйти на экран входа, не сняв времянок, значило бы показать «вышел» тому, кто не вышел,
    // и передать чужой личности хозяйство предыдущей.
    let ушли = false;
    const { корень } = await показать(
      {
        leave: {
          kind: "refusal",
          refusal: {
            code: "no-daemon",
            why: "докер на этой машине не отвечает, а времянки снимает он",
            ways: ["проверь, что сокет докера отдан контроллеру при подъёме"],
            said: "control",
          },
        },
      },
      () => (ушли = true),
    );

    нажать(корень, "Выйти");
    const блок = await дождаться(
      () => корень.querySelector<HTMLElement>("[data-action='leave'] [data-refusal='no-daemon']"),
      "отказ выхода",
    );

    expect(ушли).toBe(false);
    expect(блок.textContent).toContain("времянки снимает он");
    expect(корень.querySelector("[data-me-name]")).not.toBeNull();
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

  it("у записанного поля не показывается ни адреса, ни состояния — их пока нет", async () => {
    // Поле лежит в скоупе формой мира (`имя` · `адрес` · `состояние`), и пока оно только
    // записано, две последние пусты. Пустая строка рядом с именем говорила бы, что что-то
    // отвалилось, — поэтому её нет вовсе, а появятся значения — появится и строка.
    const { корень } = await показать({ fields: ok([{ name: "дом", addr: "", state: "" }]) });
    const строка = корень.querySelector<HTMLElement>("[data-field='дом']")!;

    expect(строка.querySelector("[data-field-addr]")).toBeNull();
    expect(строка.querySelector("[data-field-state]")).toBeNull();
    expect(строка.textContent?.trim()).toBe("дом");
  });
});

describe("источники ресурса", () => {
  it("пусто — законное состояние, и сказано словами", async () => {
    // До ступени 2 список пустым не бывал: в нём всегда стоял ресурс «здесь». Теперь он
    // берётся из СКОУПА, и у свежей личности территорий ноль (`WORLD2-132`).
    const { корень } = await показать({ resources: ok([]) });
    const блок = корень.querySelector<HTMLElement>("[data-block='resources']")!;

    expect(блок.querySelector("[data-state='empty']")).not.toBeNull();
    expect(блок.querySelector("[data-refusal]")).toBeNull();
    expect(блок.textContent).toContain("законное состояние");
  });

  it("машины контроллера в списке нет — и своего «здесь» экран не рисует", async () => {
    // Контроллер времянка (`WORLD2` 1.9), и своё хозяйство в личность юзера он не
    // подмешивает. Вернётся сюда строка «здесь стоит контроллер» — проба покраснеет.
    const { корень } = await показать({ resources: ok([УЧАСТОК, МОЛЧУН]) });

    expect(корень.textContent).not.toContain("здесь стоит контроллер");
    expect(корень.querySelector("[data-resource='here']")).toBeNull();
    expect(корень.textContent).not.toContain("изнутри машины не известен");
  });

  it("у участка показан адрес, который назвал юзер", async () => {
    const { корень } = await показать({ resources: ok([УЧАСТОК]) });
    const строка = корень.querySelector<HTMLElement>(`[data-resource='${УЧАСТОК.name}']`)!;

    expect(строка.textContent).toContain(УЧАСТОК.addr);
  });

  it("про САМ ресурс сказано отдельно от того, что на нём стоит", async () => {
    // Ресурс — машина, до которой дотянулись, а не «машина с дверью» (`WORLD2-131`). Вопросов
    // два, и на экране их тоже два: «отвечает ли машина» и «что на ней поднято».
    const { корень } = await показать({ resources: ok([УЧАСТОК]) });
    const строка = корень.querySelector<HTMLElement>(`[data-resource='${УЧАСТОК.name}']`)!;

    expect(строка.querySelector("[data-reach]")?.textContent).toBe(УЧАСТОК.reach);
    expect(строка.querySelector("[data-things]")?.getAttribute("data-things")).toBe("list");
    expect(строка.querySelector(`[data-thing='${УЧАСТОК.things![0]!.name}']`)).not.toBeNull();
  });

  it("«не спросили» и «спросили, там пусто» показаны РАЗНЫМИ ответами", async () => {
    // Молчащий ресурс — не пустая машина. Одна строка на оба случая сказала бы человеку, что на
    // недоступной машине ничего не стоит, — а этого мы не знаем (`WORLD2` 4.2).
    const { корень } = await показать({
      resources: ok([
        { ...МОЛЧУН, name: "молчит", things: null },
        { ...МОЛЧУН, name: "пусто", reach: "отвечает", things: [] },
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
          ...УЧАСТОК,
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
    const { корень } = await показать({ resources: ok([УЧАСТОК, МОЛЧУН]) });
    const строка = корень.querySelector<HTMLElement>(`[data-resource='${МОЛЧУН.name}']`)!;
    const факты = [...строка.querySelectorAll("dt")].map((dt) => dt.textContent);

    expect(факты).toEqual(["адрес", "ресурс", "вещи"]);
  });

  it("человек называет ТРИ вещи: имя участка, адрес и креды", async () => {
    // Имя участка называет юзер, и мир его не выдумывает и из адреса не выводит
    // (`WORLD2` 2.5 п. 11): на нём стоит адрес локации. Креды теперь обязательны — без них
    // контроллер отказывает (`no-creds`).
    const { корень, звонки } = await показать({ resources: ok([УЧАСТОК]) });
    expect(корень.querySelector("[data-count-resources]")?.textContent).toBe("1");

    ввести(корень, "Имя", "home");
    ввести(корень, "Адрес", "world@10.8.0.6");
    ввести(корень, "Приватный ключ", "ключ целиком");
    нажать(корень, "Добавить ресурс");
    await осесть();

    expect(звонки.filter((з) => з.ручка === "addResource")).toEqual([
      {
        ручка: "addResource",
        чем: {
          name: "home",
          addr: "world@10.8.0.6",
          creds: { kind: "key", value: "ключ целиком" },
        },
      },
    ]);
  });

  it("после добавления источников видно два — это главное, что должно быть видно глазами", async () => {
    const { корень, звонки } = await показать({ resources: ok([УЧАСТОК]) });

    ввести(корень, "Имя", "home");
    ввести(корень, "Адрес", "world@10.8.0.6");
    ввести(корень, "Приватный ключ", "ключ целиком");
    нажать(корень, "Добавить ресурс");
    await осесть();

    expect(корень.querySelector("[data-count-resources]")?.textContent).toBe("2");
    expect(корень.querySelector(`[data-resource='${МОЛЧУН.name}']`)).not.toBeNull();
    // Список берётся из ответа на добавление — второй раз контроллер не спрашивается.
    expect(звонки.filter((з) => з.ручка === "resources")).toHaveLength(1);
  });

  it("креды двух видов, и вид называет человек — не пульт по виду строки", async () => {
    // `WORLD2-141`: два вида, как в PuTTY. Угаданный вид однажды примет ключ за пароль, и
    // разбираться человек будет с отказом ssh, а не с нашей догадкой.
    const { корень, звонки } = await показать({ resources: ok([УЧАСТОК]) });

    ввести(корень, "Имя", "home");
    ввести(корень, "Адрес", "world@10.8.0.6");
    нажать(корень, "Пароль машины");
    ввести(корень, "Пароль машины", "пароль рута");
    нажать(корень, "Добавить ресурс");
    await осесть();

    expect(звонки.filter((з) => з.ручка === "addResource")).toEqual([
      {
        ручка: "addResource",
        чем: {
          name: "home",
          addr: "world@10.8.0.6",
          creds: { kind: "password", value: "пароль рута" },
        },
      },
    ]);
  });

  it("ЦЕНА пути с паролем названа ДО кнопки, а не после", async () => {
    // `WORLD2-142`, решение user. Путь с паролем пишет в ЧУЖУЮ машину строку в её
    // `~/.ssh/authorized_keys`. Узнать об этом после — значит узнать поздно.
    const { корень } = await показать({ resources: ok([УЧАСТОК]) });
    const форма = корень.querySelector<HTMLElement>("[data-block='resources'] .pult__form")!;

    expect(форма.querySelector("[data-price]")).toBeNull();
    нажать(корень, "Пароль машины");

    const цена = форма.querySelector<HTMLElement>("[data-price='password']")!;
    expect(цена).not.toBeNull();
    // Ни одной кнопки действия ПЕРЕД ценой: человек читает её раньше, чем доберётся до кнопки.
    const узлы = [...форма.querySelectorAll("[data-price], button[type='submit']")];
    expect(узлы[0]).toBe(цена);
  });

  it("что изменено на ЧУЖОЙ машине — сказано словами контроллера, дословно", async () => {
    const цена =
      "на машину world@10.8.0.6 положен публичный ключ юзера (одна строка в её " +
      "~/.ssh/authorized_keys, подпись world-control)";
    const { корень } = await показать({
      resources: ok([УЧАСТОК]),
      addResource: ok({ added: МОЛЧУН, resources: [УЧАСТОК, МОЛЧУН], note: цена }),
    });

    ввести(корень, "Имя", "home");
    ввести(корень, "Адрес", "world@10.8.0.6");
    нажать(корень, "Пароль машины");
    ввести(корень, "Пароль машины", "пароль рута");
    нажать(корень, "Добавить ресурс");
    await осесть();

    expect(корень.querySelector("[data-note='machine']")?.textContent).toBe(цена);
  });

  it("занятое имя участка — отказ рядом с формой, а не молчаливая перезапись", async () => {
    // Отказ МЕХАНИКИ, а не ответ ресурса (`WORLD2` 2.3, три рода «нет»): контроллер проверяет
    // его по содержимому скоупа, до всякого докера. Пульт показывает причину и выходы как
    // есть — своих не дописывает.
    const { корень } = await показать({
      resources: ok([УЧАСТОК]),
      addResource: {
        kind: "refusal",
        refusal: {
          code: "name-taken",
          why: `участок с именем «${УЧАСТОК.name}» в твоём скоупе уже есть`,
          ways: ["назови другое имя", "посмотри, какие уже есть: GET /api/resources"],
          said: "control",
        },
      },
    });

    ввести(корень, "Имя", УЧАСТОК.name);
    ввести(корень, "Адрес", "world@10.8.0.9");
    ввести(корень, "Приватный ключ", "ключ");
    нажать(корень, "Добавить ресурс");
    const блок = await дождаться(
      () => корень.querySelector<HTMLElement>("[data-refusal='name-taken']"),
      "отказ занятого имени",
    );

    expect([...блок.querySelectorAll(".pult__ways li")]).toHaveLength(2);
    expect(корень.querySelector("[data-count-resources]")?.textContent).toBe("1");
  });

  it("отказ добавления показан рядом с формой, а экран мира остаётся на месте", async () => {
    const { корень } = await показать({
      resources: ok([УЧАСТОК]),
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

    ввести(корень, "Имя", "home");
    ввести(корень, "Адрес", "world@10.8.0.6");
    ввести(корень, "Приватный ключ", "ключ");
    нажать(корень, "Добавить ресурс");
    const блок = await дождаться(
      () => корень.querySelector<HTMLElement>("[data-refusal='no-docker']"),
      "отказ добавления",
    );

    // Чужой код оставлен как есть и назван вместе с тем, от кого пришёл.
    expect(блок.textContent).toContain("deploy/remote.sh");
    // Экран не пропал: имя и список источников на месте.
    expect(корень.querySelector("[data-me-name]")?.textContent).toBe(Я.name);
    expect(корень.querySelector(`[data-resource='${УЧАСТОК.name}']`)).not.toBeNull();
  });
});

describe("отказ одного блока не уносит остальные", () => {
  it("ресурсы отказали — имя и поля видны, у отказа есть выход", async () => {
    const отказ: Refusal = {
      code: "scope-silent",
      why: "раздача скоупа по адресу не ответила, а список территорий лежит в нём",
      ways: ["проверь, что раздача на той машине поднята и отвечает"],
      said: "control",
    };
    const { корень } = await показать({ resources: { kind: "refusal", refusal: отказ } });

    expect(корень.querySelector("[data-block='resources'] [data-refusal]")).not.toBeNull();
    expect(корень.querySelector("[data-me-name]")?.textContent).toBe(Я.name);
    expect(корень.querySelector("[data-block='fields'] [data-state='empty']")).not.toBeNull();
    expect(корень.textContent).toContain("раздача на той машине поднята");
  });
});
