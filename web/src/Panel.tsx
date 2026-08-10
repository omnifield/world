// Пульт мира, первая страница: кто сейчас в поле.
//
// Пульт — второе лицо консоли (`kb:WORLD-53`), и правило действует с первой версии: всё, что
// можно из консоли, можно из пульта; веб привилегий не получает и своей логики не заводит.
// Консоли ещё нет — значит эта версия ТОЛЬКО ЧИТАЕТ. Кнопок «создать локацию» здесь нет не
// потому, что не успели, а потому что пульт не может уметь больше консоли.
//
// Три состояния, и каждое названо словами:
//   • поле не пусто — список того, что отдала дверь;
//   • поле пусто    — законное состояние, а не поломка (`kb:WORLD-2`);
//   • отказ         — причина и выход (`kb:WORLD-31`), а не «что-то пошло не так».
//
// `data-state` на секциях — зацепка того же рода, что `data-slot` у кита: за неё держатся
// пробы и оформление, и она не зависит от текста на экране.
import { For, Match, Show, Switch, createResource } from "solid-js";
import { Button } from "@omnifield/probe-web-ui";

import { LOCATIONS_PATH, type FieldView, readField } from "./door.js";

export type PanelProps = {
  /**
   * Откуда брать поле. Подменяется пробой, чтобы каждый отказ вызывался НАРОЧНО
   * (`kb:WORLD-32`: проба, которую ни разу не заставили упасть, пробой не является).
   */
  read?: () => Promise<FieldView>;
};

export function Panel(props: PanelProps) {
  // props.read снимается один раз на монтаже: источник поля у пульта не меняется по ходу
  // жизни страницы — меняется поле, а не дверь.
  const read = props.read ?? (() => readField());
  const [view, { refetch }] = createResource(read);

  return (
    <main class="pult">
      <header class="pult__head">
        <h1>Пульт мира</h1>
        <p class="pult__sub">
          Кто сейчас в поле. Список приходит из-за двери и показывается как есть: пульт ничего
          не досчитывает и своего состояния поля не держит.
        </p>
      </header>

      <Switch
        fallback={
          <section class="pult__block" data-state="loading">
            <p>Спрашиваю дверь…</p>
          </section>
        }
      >
        <Match when={asRefusal(view())}>
          {(refusal) => (
            <section class="pult__block pult__refusal" data-state="refusal">
              <h2>Отказ: {refusal().code}</h2>
              <p class="pult__reason">{refusal().reason}</p>
              <Show when={refusal().exit}>
                {(exit) => (
                  <p class="pult__exit">
                    <b>Что делать:</b> {exit()}
                  </p>
                )}
              </Show>
              <p class="pult__who">
                произнесено: {refusal().from === "door" ? "дверью" : "пультом"}
              </p>
              <Button onClick={() => void refetch()}>Спросить снова</Button>
            </section>
          )}
        </Match>

        <Match when={asEmpty(view())}>
          <section class="pult__block" data-state="empty">
            <h2>В поле сейчас никого</h2>
            <p>
              Это законное состояние, а не поломка: незанятая роль — не недоделка. Дверь стоит,
              реестр отвечает, и он пуст — значит ни одна локация ещё не зарегистрировалась.
            </p>
            <p class="pult__hint">
              Локация попадает в этот список регистрацией и уходит со сносом — списка «на
              будущее» здесь нет. Появится первая — она будет здесь без единой правки конфига.
            </p>
            <Button onClick={() => void refetch()}>Спросить снова</Button>
          </section>
        </Match>

        <Match when={asField(view())}>
          {(locations) => (
            <section class="pult__block" data-state="field">
              <h2>
                В поле: <span data-count>{locations().length}</span>
              </h2>
              <ul class="pult__list">
                <For each={locations()}>
                  {(loc) => (
                    <li class="pult__loc" data-loc={loc.name}>
                      <a class="pult__name" href={loc.route}>
                        {loc.name}
                      </a>
                      <p class="pult__gives">{loc.gives}</p>
                      <dl class="pult__facts">
                        <dt>адрес в сети</dt>
                        <dd>
                          <code>{loc.addr}</code>
                        </dd>
                        <dt>маршрут за дверью</dt>
                        <dd>
                          <code>{loc.route}</code>
                        </dd>
                        <Show when={loc.since}>
                          {(since) => (
                            <>
                              <dt>в поле с</dt>
                              <dd>
                                <time datetime={since()}>{since()}</time>
                              </dd>
                            </>
                          )}
                        </Show>
                      </dl>
                    </li>
                  )}
                </For>
              </ul>
              <Button onClick={() => void refetch()}>Спросить снова</Button>
            </section>
          )}
        </Match>
      </Switch>

      <footer class="pult__foot">
        источник — <code>GET {LOCATIONS_PATH}</code>
      </footer>
    </main>
  );
}

// Разбор состояния держим тремя маленькими функциями, а не условиями в разметке: Switch
// выбирает первое истинное, и `undefined` во время загрузки не должен случайно оказаться
// «пустым полем» — это разные вещи, и путать их на экране дороже всего.

function asRefusal(view: FieldView | undefined) {
  return view?.kind === "refusal" ? view.refusal : undefined;
}

function asEmpty(view: FieldView | undefined) {
  return view?.kind === "field" && view.locations.length === 0;
}

function asField(view: FieldView | undefined) {
  return view?.kind === "field" && view.locations.length > 0 ? view.locations : undefined;
}
