// Пять примитивов пульта: кнопка, поле, подпись, ввод, многострочный ввод.
//
// Раньше они приезжали китом `@omnifield/probe-web-ui` — пакетом, который лежит на складе
// ВНУТРИ мира. Получался круг: чтобы войти в мир, нужен инструмент; чтобы был инструмент,
// нужно войти (`WORLD2` 0.7 п. 6 и 3.7 — «вход в мир не зависит от инструментов мира»).
// Ходить умеют без обуви, и панель теперь тоже.
//
// **Это не дизайн-система и ею не станет.** Здесь ровно то, без чего не собрать два экрана:
// подпись, связанная с полем, управляемое значение и кнопка. Всё оформление живёт в
// `panel.css` и цепляется за `data-slot` — ту же зацепку, что была у кита, одним правилом на
// весь набор, а не на каждое поле поимённо.
//
// Сверка с рынком 2026-08-16: у Solid есть готовые безголовые киты — `@kobalte/core` (на нём
// стоял probe-web), `corvu`, `@ark-ui/solid`. НЕ берём ни один: пять примитивов дешевле
// зависимости, а кит тянул за собой ещё и правку раннера (его соседи отдают непреобразованный
// JSX файлами `.jsx`, и `vitest.config.ts` пришлось учить их пропускать через трансформ).
// Появится на экране выпадающий список, диалог или всплывашка — вот там кит окупится, и брать
// надо Kobalte: доступность у таких вещей руками не пишется.
import { createContext, createUniqueId, splitProps, useContext, type JSX } from "solid-js";

/**
 * Что поле знает о себе: свой идентификатор (по нему подпись связана с вводом), текущее
 * значение и куда его вернуть.
 *
 * Значение приходит ФУНКЦИЕЙ, а не готовой строкой: снятое один раз при создании контекста,
 * оно замёрзло бы, и ввод перестал бы обновляться. Это тот же урок, что стоит в `World.tsx`
 * над `Ожидание`.
 */
type ПолеСнаружи = {
  id: string;
  значение: () => string;
  вернуть: (значение: string) => void;
};

const Поле = createContext<ПолеСнаружи>();

function поле(кто: string): ПолеСнаружи {
  const снаружи = useContext(Поле);
  if (!снаружи) {
    // Падаем внятно, а не молча несвязанной подписью: подпись без `for` не находится ни
    // человеком по клику, ни скринридером, ни пробой — и никто об этом не узнаёт.
    throw new Error(`<${кто}> стоит вне <Field>: подпись и ввод связывает именно поле`);
  }
  return снаружи;
}

export type FieldProps = {
  /** Текущее значение — поле управляемое, своей памяти оно не держит. */
  value: string;
  onChange: (значение: string) => void;
  class?: string;
  children: JSX.Element;
};

/**
 * Поле целиком: подпись плюс ввод. Идентификатор заводится ЗДЕСЬ и раздаётся детям — так
 * подпись и ввод не могут разъехаться, даже если полей на экране пять.
 */
export function Field(props: FieldProps) {
  const id = createUniqueId();
  return (
    <Поле.Provider
      value={{ id, значение: () => props.value, вернуть: (v) => props.onChange(v) }}
    >
      <div class={props.class} data-slot="field">
        {props.children}
      </div>
    </Поле.Provider>
  );
}

/** Подпись поля. Связана с вводом через `for` — так же, как её находит человек и скринридер. */
export function Label(props: { children: JSX.Element }) {
  const своё = поле("Label");
  return (
    <label data-slot="label" for={своё.id}>
      {props.children}
    </label>
  );
}

export type InputProps = Omit<
  JSX.InputHTMLAttributes<HTMLInputElement>,
  "id" | "value" | "onInput"
>;

/** Однострочный ввод. Значение и обработчик берутся у поля, остальное — как у обычного input. */
export function Input(props: InputProps) {
  const своё = поле("Input");
  return (
    <input
      {...props}
      data-slot="input"
      id={своё.id}
      value={своё.значение()}
      onInput={(event) => своё.вернуть(event.currentTarget.value)}
    />
  );
}

export type TextareaProps = Omit<
  JSX.TextareaHTMLAttributes<HTMLTextAreaElement>,
  "id" | "value" | "onInput"
>;

/** Многострочный ввод — то же поле, только для ключа целиком. */
export function Textarea(props: TextareaProps) {
  const своё = поле("Textarea");
  return (
    <textarea
      {...props}
      data-slot="textarea"
      id={своё.id}
      value={своё.значение()}
      onInput={(event) => своё.вернуть(event.currentTarget.value)}
    />
  );
}

export type ButtonProps = JSX.ButtonHTMLAttributes<HTMLButtonElement>;

/**
 * Кнопка. По умолчанию `type="button"`, и это не мелочь: браузерное умолчание внутри формы —
 * `submit`, и кнопка «Скоупа нет — завести здесь» отправляла бы форму входа вместо того, чтобы
 * открыть форму заведения. Отправляет та кнопка, которой сказали `type="submit"`.
 */
export function Button(props: ButtonProps) {
  const [своё, остальное] = splitProps(props, ["type"]);
  return <button data-slot="button" type={своё.type ?? "button"} {...остальное} />;
}
