// Как мир говорит «нет» — один вид на все отказы пульта.
//
// Отдельным файлом, потому что отказ показывается на обоих экранах и в трёх местах экрана
// мира: собранный в одном месте, он не разъедется по формам, а вместе с ним не разъедется и
// правило `WORLD2` 2.3 — код машине, причина и ВЫХОДЫ человеку.
//
// Чего здесь нет намеренно:
//   • слов «что-то пошло не так» — это отказ без причины, то есть тупик;
//   • красной заливки — красным красят поломку, а отказ мира это работающее правило: кайма;
//   • дописанных пультом выходов к чужому отказу — контроллер уже назвал свои, и второй
//     набор «что делать» рядом с первым означает, что человеку сказали два разных ответа.
import { For, Show } from "solid-js";
import { Button } from "@omnifield/probe-web-ui";

import type { Refusal } from "./control.js";

export type RefusalViewProps = {
  refusal: Refusal;
  /** Кнопка под отказом — там, где выход можно сделать отсюда. Нет обработчика — нет кнопки. */
  onWay?: () => void;
  wayLabel?: string;
};

export function RefusalView(props: RefusalViewProps) {
  return (
    <div class="pult__refusal" data-state="refusal" data-refusal={props.refusal.code}>
      <p class="pult__code">Отказ: {props.refusal.code}</p>
      <p class="pult__reason">{props.refusal.why}</p>

      <Show when={props.refusal.ways.length > 0}>
        <div class="pult__exit">
          <b>Что делать:</b>
          <ul class="pult__ways">
            <For each={props.refusal.ways}>{(way) => <li>{way}</li>}</For>
          </ul>
        </div>
      </Show>

      <p class="pult__who">
        произнесено: {props.refusal.said === "control" ? "контроллером" : "пультом"}
        <Show when={props.refusal.from}>
          {(tool) => <span> · код пришёл от «{tool()}» и оставлен как есть</span>}
        </Show>
      </p>

      <Show when={props.onWay}>
        {(way) => <Button onClick={() => way()()}>{props.wayLabel ?? "Попробовать снова"}</Button>}
      </Show>
    </div>
  );
}
