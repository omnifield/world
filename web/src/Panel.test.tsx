// Пробы экрана: три состояния, и каждое проверяется по тому, ЧТО написано человеку, а не по
// тому, что компонент не упал (`kb:WORLD-32`).
//
// Отдельно стережём главное правило первой версии: пульт не может уметь больше консоли, а
// консоли ещё нет — значит на экране нет ни одной кнопки управления полем.
import { afterEach, describe, expect, it } from "vitest";
import { render } from "solid-js/web";

import { Panel } from "./Panel.jsx";
import type { FieldView } from "./door.js";

let dispose: (() => void) | undefined;
let host: HTMLDivElement | undefined;

afterEach(() => {
  dispose?.();
  host?.remove();
  dispose = undefined;
  host = undefined;
});

/** Монтирует пульт на заданном ответе двери и ждёт, пока уедет состояние загрузки. */
async function показать(view: FieldView): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  dispose = render(() => <Panel read={async () => view} />, host);

  for (let i = 0; i < 50; i += 1) {
    const block = host.querySelector<HTMLElement>("[data-state]");
    if (block && block.dataset.state !== "loading") return block;
    await new Promise((done) => setTimeout(done, 0));
  }
  throw new Error("пульт не ушёл из состояния загрузки");
}

const ЛОКАЦИЯ = {
  name: "baser",
  addr: "baser:3500",
  gives: "консоль продукта: кладёт обвесы в клон",
  since: "2026-08-10T19:00:00Z",
  route: "/baser/",
};

describe("список есть", () => {
  it("показывает имя, что даёт, адрес и ссылку — по строке на локацию", async () => {
    const block = await показать({
      kind: "field",
      locations: [ЛОКАЦИЯ, { ...ЛОКАЦИЯ, name: "probe-web", route: "/probe-web/" }],
    });

    expect(block.dataset.state).toBe("field");
    expect(block.querySelectorAll(".pult__loc")).toHaveLength(2);
    expect(block.querySelector("[data-count]")?.textContent).toBe("2");

    const первая = block.querySelector<HTMLElement>('[data-loc="baser"]');
    expect(первая?.textContent).toContain(ЛОКАЦИЯ.gives);
    expect(первая?.textContent).toContain(ЛОКАЦИЯ.addr);

    const ссылка = первая?.querySelector<HTMLAnchorElement>("a.pult__name");
    expect(ссылка?.textContent).toBe("baser");
    // Ссылка ведёт по МАРШРУТУ ОТ ДВЕРИ, а не по собранному из имени адресу.
    expect(ссылка?.getAttribute("href")).toBe("/baser/");
  });

  it("локация без даты регистрации показывается без даты, а не с пустым «в поле с»", async () => {
    const block = await показать({ kind: "field", locations: [{ ...ЛОКАЦИЯ, since: "" }] });

    expect(block.textContent).toContain("baser");
    expect(block.textContent).not.toContain("в поле с");
  });
});

describe("список пуст", () => {
  it("говорит словами, что в поле никого, и не красит это отказом", async () => {
    const block = await показать({ kind: "field", locations: [] });

    expect(block.dataset.state).toBe("empty");
    expect(block.textContent).toContain("В поле сейчас никого");
    // `kb:WORLD-2`: пустая роль — законное состояние. Ни слова «ошибка», ни красной каймы.
    expect(block.classList.contains("pult__refusal")).toBe(false);
    expect(block.textContent).toContain("законное состояние");
  });

  it("пустой экран пустым не остаётся: текста в блоке заметно больше заголовка", async () => {
    // Проба стережёт ровно провал «показали ничего»: убери объяснение — и она покраснеет.
    const block = await показать({ kind: "field", locations: [] });

    expect((block.textContent ?? "").trim().length).toBeGreaterThan(120);
  });
});

describe("дверь недоступна", () => {
  const отказ: FieldView = {
    kind: "refusal",
    refusal: {
      code: "door-unreachable",
      reason: "дверь не отвечает по /api/locations: Failed to fetch",
      exit: "подними сервис мира (из core/: `go run ./cmd/world`) и обнови страницу",
      from: "panel",
    },
  };

  it("называет причину и выход, а не «что-то пошло не так»", async () => {
    const block = await показать(отказ);

    expect(block.dataset.state).toBe("refusal");
    expect(block.textContent).toContain("дверь не отвечает");
    expect(block.querySelector(".pult__exit")?.textContent).toContain("go run ./cmd/world");
    expect(block.textContent).toContain("door-unreachable");
    expect(block.textContent).not.toContain("что-то пошло не так");
  });

  it("говорит, КТО произнёс отказ — пульт или дверь", async () => {
    const свой = await показать(отказ);
    expect(свой.querySelector(".pult__who")?.textContent).toContain("пультом");

    dispose?.();
    host?.remove();

    const чужой = await показать({
      kind: "refusal",
      refusal: { code: "unknown-endpoint", reason: "такой ручки нет; есть GET /api/locations", exit: "", from: "door" },
    });
    expect(чужой.querySelector(".pult__who")?.textContent).toContain("дверью");
  });

  it("отказ двери показывается без пустого блока «что делать»", async () => {
    // Выход у отказа двери назван внутри её же текста. Пустой заголовок «Что делать:» без
    // содержимого — это ровно та серая кнопка без объяснения, которую канон запрещает.
    const block = await показать({
      kind: "refusal",
      refusal: { code: "name-taken", reason: "имя занято — возьми другое либо сними ту", exit: "", from: "door" },
    });

    expect(block.querySelector(".pult__exit")).toBeNull();
    expect(block.textContent).not.toContain("Что делать");
  });
});

describe("пульт не умеет больше консоли", () => {
  it("на экране нет управления полем — только «Спросить снова»", async () => {
    // `kb:WORLD-53`: всё, что можно из консоли, можно из пульта. Консоли ещё нет — значит
    // и в пульте управления нет. Появится кнопка раньше консоли — проба покраснеет.
    for (const view of [
      { kind: "field", locations: [ЛОКАЦИЯ] },
      { kind: "field", locations: [] },
    ] satisfies FieldView[]) {
      const block = await показать(view);
      const кнопки = [...block.querySelectorAll("button")].map((b) => b.textContent?.trim());

      expect(кнопки).toEqual(["Спросить снова"]);
      dispose?.();
      host?.remove();
    }
  });
});
