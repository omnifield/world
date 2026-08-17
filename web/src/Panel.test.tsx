// Пробы пульта целиком: два экрана и переход между ними.
//
// Пульт — это та же сборка мира, повёрнутая к человеку, а не второй мир на фронте. Отсюда
// главное, что здесь стерегут: до входа пульт НИЧЕГО у мира не спрашивает, потому что до
// входа ни ресурсов, ни полей не существует (`WORLD2-102`).
import { afterEach, describe, expect, it } from "vitest";

import { Panel } from "./Panel.jsx";
import type { Answer, Control, Fetcher, Field, Identity, Resource, Session } from "./control.js";
import { liveControl } from "./control.js";
import { СВЕЖИЙ_СКОУП, изКонтракта } from "./probe-contract.js";
import { дождаться, нажать, осесть, смонтировать } from "./probe-dom.jsx";

let проба: { корень: HTMLElement; снять: () => void } | undefined;

afterEach(() => {
  проба?.снять();
  проба = undefined;
});

// Образцы — из контракта соседа (`probe-contract.ts`), а не написанные рукой.
const ВОШЁЛ: Session = { ...изКонтракта<Omit<Session, "since">>("token"), since: "" };

const Я: Identity = { ...ВОШЁЛ, since: "2026-08-14T19:00:00Z" };

const ok = <T,>(value: T): Answer<T> => ({ kind: "ok", value });

function контроллер(правки: Partial<Control> = {}) {
  const ручки: string[] = [];
  const control: Control = {
    async enter() {
      ручки.push("enter");
      return ok(ВОШЁЛ);
    },
    async me() {
      ручки.push("me");
      return ok(Я);
    },
    async resources() {
      ручки.push("resources");
      return ok([] as Resource[]);
    },
    async addResource() {
      throw new Error("не звали");
    },
    async fields() {
      ручки.push("fields");
      return ok([] as Field[]);
    },
    async addField() {
      throw new Error("не звали");
    },
    ...правки,
  };
  return { control, ручки };
}

describe("до входа", () => {
  it("виден экран входа, а экрана мира нет вовсе", async () => {
    const { control } = контроллер();
    проба = смонтировать(() => <Panel control={control} />);
    await осесть();

    expect(проба.корень.querySelector("[data-screen='sign-in']")).not.toBeNull();
    expect(проба.корень.querySelector("[data-screen='world']")).toBeNull();
  });

  it("пульт не спрашивает у контроллера ничего — спрашивать пока не о чем", async () => {
    const { control, ручки } = контроллер();
    проба = смонтировать(() => <Panel control={control} />);
    await осесть();

    expect(ручки).toEqual([]);
  });
});

describe("после входа", () => {
  it("экран сменяется на мир", async () => {
    const { control } = контроллер();
    проба = смонтировать(() => <Panel control={control} />);

    нажать(проба.корень, "Войти");
    await дождаться(
      () => проба?.корень.querySelector("[data-screen='world']"),
      "экран мира",
    );

    expect(проба.корень.querySelector("[data-screen='sign-in']")).toBeNull();
  });

  it("скоуп завели прямо сейчас — сказано, что он заведён здесь", async () => {
    // Молчание тут выглядело бы как «вошёл в существующий»: человек не узнал бы, что на
    // ресурсе контроллера только что появилась его личность.
    const { control } = контроллер({
      async enter() {
        return ok({ ...ВОШЁЛ, created: true });
      },
    });
    проба = смонтировать(() => <Panel control={control} />);

    нажать(проба.корень, "Войти");
    const сказано = await дождаться(
      () => проба?.корень.querySelector<HTMLElement>("[data-state='created']"),
      "слова о заведённом скоупе",
    );

    expect(сказано.textContent).toContain("заведён здесь");
  });
});

describe("первый вход в мир — свежесозданный скоуп", () => {
  /**
   * Контроллер для сквозной пробы: отвечает НАСТОЯЩИМИ телами по путям, а разбирает их сам
   * пульт (`liveControl`). Именно этой связки не хватало — фальшивый `Control` подсовывал уже
   * разобранное значение и проскакивал мимо разбора, где и жил дефект (`WORLD2-135`).
   */
  const отвечает = (по: Record<string, unknown>): Fetcher => async (path) => {
    const тело = JSON.stringify(по[path] ?? {});
    return { ok: true, status: 200, statusText: "", text: async () => тело } as unknown as Response;
  };

  it("вход на свежем скоупе доходит до экрана мира, а бренд назван словами", async () => {
    // Приёмка `WORLD2-135` целиком, насколько её берёт jsdom: живой ответ свежего скоупа →
    // разбор пульта → экран мира. Живьём, на настоящем контроллере, это по-прежнему отдельная
    // проверка — здесь нет ни браузера, ни докера.
    const { token: _, ...личность } = СВЕЖИЙ_СКОУП;
    const control = liveControl(
      отвечает({
        "/api/session": СВЕЖИЙ_СКОУП,
        "/api/me": личность,
        "/api/resources": { resources: [] },
        "/api/fields": { fields: [] },
      }),
      () => {},
    );
    проба = смонтировать(() => <Panel control={control} />);

    нажать(проба.корень, "Войти");
    const мир = await дождаться(
      () => проба?.корень.querySelector<HTMLElement>("[data-screen='world']"),
      "экран мира на свежем скоупе",
    );
    await осесть();

    expect(мир.querySelector("[data-me-name]")?.textContent).toBe(СВЕЖИЙ_СКОУП.name);
    expect(мир.querySelector("[data-me-brand] [data-state='empty']")).not.toBeNull();
    // Отказа нет нигде: пустое — законное состояние, а не поломка ответа.
    expect(мир.querySelector("[data-refusal]")).toBeNull();
  });
});

describe("сессии контроллера не стало", () => {
  it("отказ «не входили» возвращает на экран входа кнопкой, а не тупиком", async () => {
    // Сессия контроллера живёт в памяти его процесса: он перезапустился — вход надо повторить.
    // Без этого выхода человек остался бы на экране мира, который не может ничего показать.
    const { control } = контроллер({
      async me() {
        return {
          kind: "refusal",
          refusal: {
            code: "not-signed-in",
            why: "в скоуп ещё не входили",
            ways: ["войди: POST /api/session"],
            said: "control",
          },
        };
      },
    });
    проба = смонтировать(() => <Panel control={control} />);

    нажать(проба.корень, "Войти");
    await дождаться(
      () => проба?.корень.querySelector("[data-refusal='not-signed-in']"),
      "отказ «не входили»",
    );

    нажать(проба.корень, "Войти заново");
    await осесть();

    expect(проба.корень.querySelector("[data-screen='sign-in']")).not.toBeNull();
    expect(проба.корень.querySelector("[data-screen='world']")).toBeNull();
  });
});
