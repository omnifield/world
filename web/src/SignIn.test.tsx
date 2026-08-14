// Пробы экрана входа.
//
// Проверяем не «компонент не упал», а ЧТО написано человеку и ЧТО ушло в контроллер: экран
// входа — единственное место, где человек называет адрес своего скоупа и креды, и ошибка тут
// стоит ему входа в мир.
import { afterEach, describe, expect, it } from "vitest";

import { SignIn } from "./SignIn.jsx";
import type { Answer, Control, Session } from "./control.js";
import { ввести, дождаться, нажать, осесть, смонтировать } from "./probe-dom.jsx";

let проба: { корень: HTMLElement; снять: () => void } | undefined;

afterEach(() => {
  проба?.снять();
  проба = undefined;
});

const ВОШЁЛ: Session = {
  name: "егор",
  brand: "омнифилд",
  scope: { addr: "/scope/егор", here: true, path: "/scope/егор" },
  since: "",
  created: false,
  token: "метка-1",
};

/** Контроллер для пробы: помнит, чем его позвали, и отвечает тем, что велели. */
function контроллер(ответ: Answer<Session> | Array<Answer<Session>>) {
  const очередь = Array.isArray(ответ) ? [...ответ] : [ответ];
  const звонки: unknown[] = [];
  const control = {
    async enter(req: unknown) {
      звонки.push(req);
      return очередь.length > 1 ? очередь.shift()! : очередь[0]!;
    },
  } as unknown as Control;
  return { control, звонки };
}

const ОТКАЗ_НЕТ_СКОУПА: Answer<Session> = {
  kind: "refusal",
  refusal: {
    code: "no-scope",
    why: "по адресу /scope/егор скоупа нет — дотянулись, а личности там не лежит",
    ways: ["завести его здесь", "или назови адрес, где скоуп уже лежит"],
    said: "control",
  },
};

describe("до входа не существует ничего", () => {
  it("на экране только адрес скоупа и креды — ни ресурсов, ни полей, ни имени", async () => {
    // `WORLD2-102`: «больше на нём нет ничего: ни полей, ни ресурсов — до входа их не
    // существует». Появится здесь список — проба покраснеет.
    const { control } = контроллер({ kind: "ok", value: ВОШЁЛ });
    проба = смонтировать(() => <SignIn control={control} onEntered={() => {}} />);

    const подписи = [...проба.корень.querySelectorAll("label")].map((l) => l.textContent?.trim());
    expect(подписи).toEqual(["Где лежит твой скоуп", "Креды к ресурсу"]);
    expect(проба.корень.textContent).not.toContain("Источники ресурса");
    expect(проба.корень.textContent).not.toContain("Поля");
  });
});

describe("вход", () => {
  it("отдаёт контроллеру ровно то, что назвал человек, и сообщает о входе", async () => {
    const { control, звонки } = контроллер({ kind: "ok", value: ВОШЁЛ });
    let вошёл: Session | undefined;
    проба = смонтировать(() => <SignIn control={control} onEntered={(s) => (вошёл = s)} />);

    ввести(проба.корень, "Где лежит твой скоуп", "world@10.8.0.5:/srv/scope");
    ввести(проба.корень, "Креды к ресурсу", "ключ целиком");
    нажать(проба.корень, "Войти");
    await осесть();

    expect(звонки).toEqual([{ addr: "world@10.8.0.5:/srv/scope", creds: "ключ целиком" }]);
    expect(вошёл?.name).toBe("егор");
  });
});

describe("отказ", () => {
  it("называет причину и ВСЕ выходы, а не код и не «что-то пошло не так»", async () => {
    const { control } = контроллер(ОТКАЗ_НЕТ_СКОУПА);
    проба = смонтировать(() => <SignIn control={control} onEntered={() => {}} />);

    нажать(проба.корень, "Войти");
    const блок = await дождаться(
      () => проба?.корень.querySelector<HTMLElement>("[data-refusal]"),
      "блок отказа",
    );

    expect(блок.dataset.refusal).toBe("no-scope");
    expect(блок.textContent).toContain("скоупа нет");
    const выходы = [...блок.querySelectorAll(".pult__ways li")].map((li) => li.textContent);
    expect(выходы).toEqual(["завести его здесь", "или назови адрес, где скоуп уже лежит"]);
    expect(блок.textContent).not.toContain("что-то пошло не так");
  });

  it("говорит, КТО произнёс отказ — контроллер или сам пульт", async () => {
    const { control } = контроллер({
      kind: "refusal",
      refusal: {
        code: "control-unreachable",
        why: "контроллер не отвечает по /api/session",
        ways: ["подними контроллер: ./control/up.sh"],
        said: "panel",
      },
    });
    проба = смонтировать(() => <SignIn control={control} onEntered={() => {}} />);

    нажать(проба.корень, "Войти");
    const блок = await дождаться(
      () => проба?.корень.querySelector<HTMLElement>(".pult__who"),
      "кто произнёс",
    );

    expect(блок.textContent).toContain("пультом");
  });

  it("отказ без выходов показывается без пустого блока «что делать»", async () => {
    // Пустой заголовок «Что делать:» без содержимого — это ровно та серая кнопка без
    // объяснения, которую канон запрещает.
    const { control } = контроллер({
      kind: "refusal",
      refusal: { code: "bad-address", why: "в адресе нет пути", ways: [], said: "control" },
    });
    проба = смонтировать(() => <SignIn control={control} onEntered={() => {}} />);

    нажать(проба.корень, "Войти");
    const блок = await дождаться(
      () => проба?.корень.querySelector<HTMLElement>("[data-refusal]"),
      "блок отказа",
    );

    expect(блок.querySelector(".pult__exit")).toBeNull();
    expect(блок.textContent).not.toContain("Что делать");
  });
});

describe("скоупа нет вовсе — путь «завести здесь»", () => {
  it("виден до всякого отказа: человеку без скоупа не нужно сперва получить «нет»", async () => {
    const { control } = контроллер({ kind: "ok", value: ВОШЁЛ });
    проба = смонтировать(() => <SignIn control={control} onEntered={() => {}} />);

    const кнопки = [...проба.корень.querySelectorAll("button")].map((b) => b.textContent?.trim());
    expect(кнопки).toContain("Скоупа нет — завести здесь");
  });

  it("после отказа no-scope форма заведения открывается с ТЕМ ЖЕ адресом", async () => {
    const { control, звонки } = контроллер([
      ОТКАЗ_НЕТ_СКОУПА,
      { kind: "ok", value: { ...ВОШЁЛ, created: true } },
    ]);
    let вошёл: Session | undefined;
    проба = смонтировать(() => <SignIn control={control} onEntered={(s) => (вошёл = s)} />);

    ввести(проба.корень, "Где лежит твой скоуп", "/scope/егор");
    нажать(проба.корень, "Войти");
    await дождаться(
      () => проба?.корень.querySelector<HTMLElement>("[data-refusal]"),
      "блок отказа",
    );

    нажать(проба.корень, "Завести скоуп здесь");
    ввести(проба.корень, "Имя", "егор");
    ввести(проба.корень, "Бренд", "омнифилд");
    нажать(проба.корень, "Завести здесь");
    await осесть();

    // Адрес не набирается заново — второй запрос уходит с тем же.
    expect(звонки[1]).toEqual({
      addr: "/scope/егор",
      creds: "",
      create: true,
      name: "егор",
      brand: "омнифилд",
    });
    expect(вошёл?.created).toBe(true);
  });
});
