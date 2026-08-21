// Пробы пульта целиком: два экрана и переходы между ними.
//
// Пульт — это та же сборка мира, повёрнутая к человеку, а не второй мир на фронте. Отсюда
// главное, что здесь стерегут: до входа пульт НИЧЕГО у мира не спрашивает, потому что до
// входа ни ресурсов, ни полей не существует (`WORLD2-102`), — и то, ради чего делалась
// ступень 2: вышел, вошёл другой личностью и видишь ЕЁ, а не чужое (`WORLD2-132`).
import { afterEach, describe, expect, it } from "vitest";

import { Panel } from "./Panel.jsx";
import type { Answer, Control, Fetcher, Field, Identity, Resource, Session } from "./control.js";
import { liveControl } from "./control.js";
import { изКонтракта, свежийСкоуп } from "./probe-contract.js";
import { ввести, дождаться, нажать, открыть, осесть, смонтировать } from "./probe-dom.jsx";

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
    async createScope() {
      ручки.push("createScope");
      return ok({ ...ВОШЁЛ, created: true });
    },
    async leave() {
      ручки.push("leave");
      return ok({ note: "вышли: времянки сняты, скоуп не тронут" });
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
    await дождаться(() => проба?.корень.querySelector("[data-screen='world']"), "экран мира");

    expect(проба.корень.querySelector("[data-screen='sign-in']")).toBeNull();
  });

  it("скоуп завели прямо сейчас — сказано, по какому АДРЕСУ он лежит", async () => {
    // Молчание тут выглядело бы как «вошёл в существующий». А слова «заведён здесь» ушли
    // вместе с ходом: контроллер личности не держит, и скоуп лежит там, где его назвал юзер
    // (`WORLD2` 3.7, `WORLD2-132`).
    const { control } = контроллер();
    проба = смонтировать(() => <Panel control={control} />);

    открыть(проба.корень, "Завести скоуп");
    нажать(проба.корень, "Завести скоуп");
    const сказано = await дождаться(
      () => проба?.корень.querySelector<HTMLElement>("[data-state='created']"),
      "слова о заведённом скоупе",
    );

    expect(сказано.textContent).toContain(ВОШЁЛ.scope.addr);
    expect(сказано.textContent).not.toContain("заведён здесь");
  });
});

describe("первый вход в мир — свежесозданный скоуп", () => {
  /**
   * Контроллер для сквозной пробы: отвечает НАСТОЯЩИМИ телами по глаголу и пути, а разбирает
   * их сам пульт (`liveControl`). Именно этой связки не хватало — фальшивый `Control`
   * подсовывал уже разобранное значение и проскакивал мимо разбора, где и жил дефект
   * (`WORLD2-135`).
   *
   * Ответы лежат очередью на каждый ключ: экран мира монтируется заново на каждый вход, и
   * второй его вопрос обязан получить ответ ВТОРОЙ личности, а не повтор первого.
   */
  const контур = (таблица: Record<string, unknown[]>) => {
    const спрошено: string[] = [];
    const шаг: Record<string, number> = {};
    const fetch: Fetcher = async (path, init) => {
      const ключ = `${init?.method ?? "GET"} ${path}`;
      спрошено.push(ключ);
      const очередь = таблица[ключ];
      if (!очередь) throw new Error(`проба не готовила ответа на ${ключ}`);
      const n = шаг[ключ] ?? 0;
      шаг[ключ] = n + 1;
      const тело = JSON.stringify(очередь[Math.min(n, очередь.length - 1)]);
      return { ok: true, status: 200, statusText: "", text: async () => тело } as unknown as Response;
    };
    return { fetch, спрошено };
  };

  const СВЕЖИЙ = свежийСкоуп<Omit<Session, "since">>();

  it("вход на свежем скоупе доходит до экрана мира, а бренд назван словами", async () => {
    // Приёмка `WORLD2-135` целиком, насколько её берёт jsdom: живой ответ свежего скоупа →
    // разбор пульта → экран мира. Живьём, на настоящем контроллере и в настоящем браузере,
    // тот же путь гоняется сквозным прогоном (`e2e/pult.spec.ts`, «пустой бренд не ломает
    // вход»); здесь он остаётся потому, что jsdom берёт его за миллисекунды и на каждом
    // сохранении, а сквозной — за секунды и подъёмом двух соседей.
    const { token: _, ...личность } = СВЕЖИЙ;
    const { fetch } = контур({
      "POST /api/session": [СВЕЖИЙ],
      "GET /api/me": [личность],
      "GET /api/resources": [{ resources: [] }],
      "GET /api/fields": [{ fields: [] }],
    });
    проба = смонтировать(() => <Panel control={liveControl(fetch, () => {})} />);

    нажать(проба.корень, "Войти");
    const мир = await дождаться(
      () => проба?.корень.querySelector<HTMLElement>("[data-screen='world']"),
      "экран мира на свежем скоупе",
    );
    await осесть();

    expect(мир.querySelector("[data-me-name]")?.textContent).toBe(СВЕЖИЙ.name);
    expect(мир.querySelector("[data-me-brand] [data-state='empty']")).not.toBeNull();
    // Территорий у свежего скоупа ноль — и это состояние, а не отказ: ресурса «здесь»
    // больше нет, а своих участков юзер ещё не заводил.
    expect(мир.querySelector("[data-block='resources'] [data-state='empty']")).not.toBeNull();
    // Отказа нет нигде: пустое — законное состояние, а не поломка ответа.
    expect(мир.querySelector("[data-refusal]")).toBeNull();
  });

  it("вышел, вошёл другой личностью — видны ЕЁ территории, а не чужие", async () => {
    // ТО, РАДИ ЧЕГО ДЕЛАЛАСЬ СТУПЕНЬ 2 (`WORLD2-132`, пп. 6 приёмки). Увидь пульт после смены
    // личности чужой участок — значит состояние осело в контроллере, и «личность» не значит
    // ничего. Здесь это проверяется на своей стороне шва: настоящие тела, настоящий разбор,
    // экран. Сквозной прогон эту пробу не подменяет: у стенда один скоуп, второй личности
    // там взяться неоткуда (`e2e/stend.ts`), — а вход и выход поодиночке он гоняет браузером.
    const первый = {
      ...СВЕЖИЙ,
      name: "тест",
      scope: { addr: "http://10.8.0.9:8070/", host: "10.8.0.9" },
      token: "метка-теста",
    };
    const второй = { ...ВОШЁЛ, created: false, token: "метка-егора" };
    const участокТеста = {
      name: "проба",
      addr: "world@10.8.0.9",
      reach: "отвечает",
      things: [] as unknown[],
    };
    const участокЕгора = { name: "vps", addr: "world@10.8.0.5", reach: "отвечает", things: null };

    const { fetch, спрошено } = контур({
      "POST /api/scope": [первый],
      "POST /api/session": [второй],
      "DELETE /api/session": [{ out: true, note: "вышли: времянки сняты, скоуп не тронут" }],
      "GET /api/me": [первый, второй],
      "GET /api/resources": [{ resources: [участокТеста] }, { resources: [участокЕгора] }],
      "GET /api/fields": [{ fields: [] }],
    });
    проба = смонтировать(() => <Panel control={liveControl(fetch, () => {})} />);

    // 1. Завожу тестовый скоуп прямо из пульта — по адресу, на названной машине.
    открыть(проба.корень, "Завести скоуп");
    ввести(проба.корень, "Где лежит твой скоуп", первый.scope.addr);
    ввести(проба.корень, "Пароль скоупа", "пароль-теста");
    ввести(проба.корень, "Имя", "тест");
    ввести(проба.корень, "Имя участка", "проба");
    ввести(проба.корень, "Адрес машины", "world@10.8.0.9");
    ввести(проба.корень, "Приватный ключ", "ключ целиком");
    нажать(проба.корень, "Завести скоуп");

    await дождаться(
      () => проба?.корень.querySelector(`[data-resource='${участокТеста.name}']`),
      "территория тестовой личности",
    );

    // 2. Выхожу — и это ручка, а не забытая метка.
    нажать(проба.корень, "Выйти");
    await дождаться(
      () => проба?.корень.querySelector("[data-screen='sign-in']"),
      "экран входа после выхода",
    );
    // Контроллер сказал, чего выход НЕ тронул, — и слова эти видны, а не проглочены.
    expect(проба.корень.querySelector("[data-state='left']")?.textContent).toContain(
      "скоуп не тронут",
    );

    // 3. Вхожу другой личностью — тем же экраном и теми же двумя полями.
    ввести(проба.корень, "Где лежит твой скоуп", второй.scope.addr);
    ввести(проба.корень, "Пароль скоупа", "пароль-егора");
    нажать(проба.корень, "Войти");
    await дождаться(
      () => проба?.корень.querySelector(`[data-resource='${участокЕгора.name}']`),
      "территория второй личности",
    );
    await осесть();

    expect(проба.корень.querySelector("[data-me-name]")?.textContent).toBe(второй.name);
    // ЧУЖОГО НЕ ВИДНО: территория прежней личности с экрана ушла вместе с ней.
    expect(проба.корень.querySelector(`[data-resource='${участокТеста.name}']`)).toBeNull();
    expect(проба.корень.querySelector("[data-refusal]")).toBeNull();
    // Выход сходил в контроллер, а не остался у пульта в памяти.
    expect(спрошено).toContain("DELETE /api/session");
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
