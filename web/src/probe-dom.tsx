// Общий обвес экранных проб: смонтировать, ввести, нажать, дождаться.
//
// Файлом рядом с пробами, а не копией в каждой: три экрана монтируются одинаково, и
// разъехавшийся помощник сделал бы пробы разными там, где они должны быть одинаковыми.
// В сборку он не попадает — его никто не импортирует из кода пульта.
//
// Ввод — это событие `input` на живом элементе, а не присвоение сигналу: поле кита
// управляемое, значение в него возвращается через `onChange`, и проба обязана ходить тем же
// путём, что и человек. Проба, дёргающая сигнал напрямую, зеленела бы на оторванной форме.
import type { JSX } from "solid-js";
import { render } from "solid-js/web";

export type Проба = {
  корень: HTMLElement;
  снять: () => void;
};

/** Монтирует кусок пульта в свежий узел документа. */
export function смонтировать(что: () => JSX.Element): Проба {
  const корень = document.createElement("div");
  document.body.append(корень);
  const dispose = render(что, корень);
  return {
    корень,
    снять: () => {
      dispose();
      корень.remove();
    },
  };
}

/** Ждёт, пока на экране появится то, что ищем. Возвращает найденное. */
export async function дождаться<T>(искать: () => T | null | undefined, что = "элемент"): Promise<T> {
  for (let i = 0; i < 100; i += 1) {
    const найдено = искать();
    if (найдено) return найдено;
    await new Promise((done) => setTimeout(done, 0));
  }
  throw new Error(`не дождались: ${что}`);
}

/** Даёт микрозадачам доехать: обещания проб разрешаются не в том же такте, что клик. */
export async function осесть(тактов = 5): Promise<void> {
  for (let i = 0; i < тактов; i += 1) {
    await new Promise((done) => setTimeout(done, 0));
  }
}

/** Ввод по ПОДПИСИ поля — так же, как его находит человек и скринридер. */
export function ввести(корень: HTMLElement, подпись: string, значение: string): void {
  const label = [...корень.querySelectorAll("label")].find(
    (l) => l.textContent?.trim() === подпись,
  );
  if (!label) throw new Error(`нет поля с подписью «${подпись}»`);

  const id = label.getAttribute("for");
  const поле = id
    ? корень.querySelector<HTMLInputElement | HTMLTextAreaElement>(`#${CSS.escape(id)}`)
    : null;
  if (!поле) throw new Error(`подпись «${подпись}» ни с чем не связана`);

  поле.value = значение;
  поле.dispatchEvent(new Event("input", { bubbles: true }));
}

/** Нажатие по надписи на кнопке. */
export function нажать(корень: HTMLElement, надпись: string): void {
  const кнопка = [...корень.querySelectorAll("button")].find((b) =>
    b.textContent?.trim().includes(надпись),
  );
  if (!кнопка) {
    const есть = [...корень.querySelectorAll("button")].map((b) => b.textContent?.trim());
    throw new Error(`нет кнопки «${надпись}»; на экране: ${JSON.stringify(есть)}`);
  }
  кнопка.click();
}
