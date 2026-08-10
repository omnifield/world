// Пробы разговора с дверью.
//
// `kb:WORLD-32`: проба, которую ни разу не заставили упасть, пробой не является. Поэтому
// счастливый путь здесь — один тест из многих, а каждый отказ вызывается НАРОЧНО: тело не
// того вида, код не тот, сеть легла. Проверяем при этом не «покраснело», а что именно сказано:
// отказ без причины и выхода — провал, а не мелочь (`kb:WORLD-31`).
import { describe, expect, it } from "vitest";

import { LOCATIONS_PATH, type Fetcher, readField } from "./door.js";

/**
 * Ответ двери для пробы. Своими руками, а не через глобальный Response: `readField` держится
 * ровно за `ok`/`status`/`statusText`/`text()`, и подделка ровно этих четырёх делает видимым,
 * какой поверхностью ответа пульт вообще пользуется.
 */
function answers(body: string, init: { status?: number; statusText?: string } = {}): Fetcher {
  const status = init.status ?? 200;
  return async (path) => {
    expect(path).toBe(LOCATIONS_PATH);
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: init.statusText ?? "",
      text: async () => body,
    } as unknown as Response;
  };
}

const ЛОКАЦИЯ = {
  name: "baser",
  addr: "baser:3500",
  gives: "консоль продукта: кладёт обвесы в клон",
  since: "2026-08-10T19:00:00Z",
  route: "/baser/",
};

describe("список есть", () => {
  it("отдаёт локации в том виде, в каком их назвала дверь", async () => {
    const view = await readField(answers(JSON.stringify({ count: 1, locations: [ЛОКАЦИЯ] })));

    expect(view).toEqual({ kind: "field", locations: [ЛОКАЦИЯ] });
  });

  it("не досчитывает маршрут сам: берёт тот, что пришёл", async () => {
    // Дверь считает маршрут из имени, и правило известно — но знать его пульту нельзя:
    // это была бы вторая истина о том, где локация достижима (`kb:WORLD-53`).
    const чужой = { ...ЛОКАЦИЯ, route: "/не-по-имени/" };
    const view = await readField(answers(JSON.stringify({ count: 1, locations: [чужой] })));

    expect(view).toEqual({ kind: "field", locations: [чужой] });
  });

  it("не требует since: без даты список показать можно, соврать датой — нет", async () => {
    const { since: _, ...безДаты } = ЛОКАЦИЯ;
    const view = await readField(answers(JSON.stringify({ count: 1, locations: [безДаты] })));

    expect(view).toEqual({ kind: "field", locations: [{ ...безДаты, since: "" }] });
  });
});

describe("список пуст", () => {
  it("пустое поле — это поле, а не отказ", async () => {
    // `kb:WORLD-2`: незанятая роль не недоделка. Спутать это с отказом значит показать
    // поломку там, где мир работает как задумано.
    const view = await readField(answers(JSON.stringify({ count: 0, locations: [] })));

    expect(view).toEqual({ kind: "field", locations: [] });
  });
});

describe("дверь недоступна", () => {
  it("сеть легла — отказ называет причину и выход", async () => {
    const view = await readField(async () => {
      throw new TypeError("Failed to fetch");
    });

    expect(view.kind).toBe("refusal");
    if (view.kind !== "refusal") return;
    expect(view.refusal.code).toBe("door-unreachable");
    expect(view.refusal.from).toBe("panel");
    expect(view.refusal.reason).toContain("Failed to fetch");
    expect(view.refusal.exit).toContain("go run ./cmd/world");
  });

  it("на месте двери кто-то другой — 502 без её формы отказа", async () => {
    const view = await readField(
      answers("<html><body>502 Bad Gateway</body></html>", {
        status: 502,
        statusText: "Bad Gateway",
      }),
    );

    expect(view.kind).toBe("refusal");
    if (view.kind !== "refusal") return;
    expect(view.refusal.code).toBe("door-not-answering");
    expect(view.refusal.reason).toContain("502");
    expect(view.refusal.exit).toContain("healthz");
  });

  it("тело чужого ответа обрезается, а не выливается на экран целиком", async () => {
    const view = await readField(answers("x".repeat(5000), { status: 500 }));

    expect(view.kind).toBe("refusal");
    if (view.kind !== "refusal") return;
    expect(view.refusal.reason.length).toBeLessThan(400);
    expect(view.refusal.reason).toContain("…");
  });
});

describe("отказ, названный самой дверью", () => {
  it("проходит насквозь и не переписывается своими словами", async () => {
    // Дверь по канону кладёт причину и выход в один detail. Переписать его здесь значило бы
    // потерять выход — поэтому свой exit пуст, и это отмечено полем from.
    const detail = "такой ручки у реестра нет; есть POST/GET /api/locations — список в core/README.md";
    const view = await readField(
      answers(JSON.stringify({ error: "unknown-endpoint", detail }), { status: 404 }),
    );

    expect(view).toEqual({
      kind: "refusal",
      refusal: { code: "unknown-endpoint", reason: detail, exit: "", from: "door" },
    });
  });

  it("похожее, но не её форма — отказ пульта, и выход он называет сам", async () => {
    const view = await readField(answers(JSON.stringify({ message: "oops" }), { status: 400 }));

    expect(view.kind).toBe("refusal");
    if (view.kind !== "refusal") return;
    expect(view.refusal.from).toBe("panel");
    expect(view.refusal.exit).not.toBe("");
  });
});

describe("ответ не той формы", () => {
  it("200, но тело не JSON — вероятнее всего это статика вместо ручки", async () => {
    const view = await readField(answers("<!doctype html><title>пульт</title>"));

    expect(view.kind).toBe("refusal");
    if (view.kind !== "refusal") return;
    expect(view.refusal.code).toBe("answer-not-json");
    expect(view.refusal.exit).toContain(LOCATIONS_PATH);
  });

  it("JSON, но не список локаций", async () => {
    const view = await readField(answers(JSON.stringify({ count: 0 })));

    expect(view.kind).toBe("refusal");
    if (view.kind !== "refusal") return;
    expect(view.refusal.code).toBe("answer-not-field");
    expect(view.refusal.reason).toContain("locations");
  });

  it("массив вместо объекта — тоже не список локаций", async () => {
    const view = await readField(answers(JSON.stringify([ЛОКАЦИЯ])));

    expect(view.kind).toBe("refusal");
    if (view.kind !== "refusal") return;
    expect(view.refusal.code).toBe("answer-not-field");
  });

  it("у локации нет имени — показывать половину строки нечестно", async () => {
    const { name: _, ...безИмени } = ЛОКАЦИЯ;
    const view = await readField(
      answers(JSON.stringify({ count: 2, locations: [ЛОКАЦИЯ, безИмени] })),
    );

    expect(view.kind).toBe("refusal");
    if (view.kind !== "refusal") return;
    expect(view.refusal.code).toBe("answer-not-field");
    // Номер элемента в причине — чтобы искать не по всему списку.
    expect(view.refusal.reason).toContain("элемента 1");
    expect(view.refusal.reason).toContain("name");
  });
});

describe("инвариант отказов пульта", () => {
  const свои: Array<[string, Fetcher]> = [
    ["сеть легла", async () => { throw new Error("ECONNREFUSED"); }],
    ["чужой 502", answers("nginx", { status: 502 })],
    ["не JSON", answers("<html>")],
    ["не список", answers("{}")],
  ];

  it.each(свои)("%s: причина и выход названы оба", async (_имя, fetcher) => {
    // Ровно то, чем отказ мира отличается от «что-то пошло не так»: пустая причина или
    // пустой выход роняют прогон, а не проходят незамеченными.
    const view = await readField(fetcher);

    expect(view.kind).toBe("refusal");
    if (view.kind !== "refusal") return;
    expect(view.refusal.from).toBe("panel");
    expect(view.refusal.reason.trim()).not.toBe("");
    expect(view.refusal.exit.trim()).not.toBe("");
  });

  it("не бросает никогда: даже если чтение тела падает", async () => {
    const view = await readField(async () => {
      return {
        ok: true,
        status: 200,
        statusText: "OK",
        text: async () => {
          throw new Error("поток оборвался");
        },
      } as unknown as Response;
    });

    // Оборванное тело доезжает как пустая строка и разбирается как «не JSON» — экран
    // получает отказ, а не необработанное исключение и белую страницу.
    expect(view.kind).toBe("refusal");
  });
});
