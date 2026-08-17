// Экран 2 — МИР. То, что записано в `WORLD2` 3.3: кто я, мои поля, мои источники ресурса.
//
// Три блока, и каждый отвечает за себя: отказ в одном не уносит экран целиком. Списки нужны
// человеку одновременно, а отказать может любой из них, и «ресурсы не спросились» не повод
// прятать имя и поля.
//
// Чего здесь нет намеренно:
//   • РЕСУРСА В ЦИФРАХ (память, ядра) — их даёт отдельный инструмент осмотра, и он сознательно
//     отложен (`WORLD2` 2.5). Показать «8 ГБ» пульту неоткуда, а выдумать — значит соврать;
//   • ВНУТРЕННОСТЕЙ ПОЛЯ — поле разбирается следующей итерацией, и здесь оно строка списка;
//   • СНЯТИЯ РЕСУРСА — у контроллера ручка есть, `WORLD2-102` её на экран не просит, а
//     необратимое действие на чужой машине тихой кнопкой рядом со списком не заводят.
import { For, Show, Switch, Match, createResource, createSignal } from "solid-js";

import type { Answer, Control, Field as WorldField, Refusal, Resource } from "./control.js";
import { RefusalView } from "./Refusal.jsx";
import { Button, Field, Input, Label, Textarea } from "./ui.jsx";

export type WorldProps = {
  control: Control;
  /**
   * Вернуться на экран входа. Зовётся из двух разных мест, и это разные события: состоявшийся
   * ВЫХОД (контроллер снял свои времянки и сказал об этом словами) и «сессии не стало» —
   * контроллер живёт в памяти процесса и мог перезапуститься.
   */
  onLeave: (сказано?: string) => void;
};

export function World(props: WorldProps) {
  // Личность перечитывается у контроллера, а не берётся из ответа входа: скоуп мог измениться
  // с другой машины, и мы обещали связь, а не снимок (`WORLD2` 1.6). Своего состояния мира
  // пульт не держит — это тот же инвариант, что и в `control.ts`.
  const [who, { refetch: askWho }] = createResource(() => props.control.me());
  const [resources, { refetch: askResources, mutate: setResources }] = createResource(() =>
    props.control.resources(),
  );
  const [fields, { refetch: askFields, mutate: setFields }] = createResource(() =>
    props.control.fields(),
  );

  return (
    <div class="pult__world" data-screen="world">
      {/* ── кто я ─────────────────────────────────────────────────────────── */}
      <section class="pult__block" data-block="me">
        <Ожидание
          answer={who()}
          отказ={(said) => (
            <RefusalView refusal={said} onWay={() => void askWho()} wayLabel="Спросить снова" />
          )}
        >
          {(me) => (
            <>
              <h2>
                Я — <span data-me-name>{me().name}</span>
              </h2>
              <dl class="pult__facts">
                <dt>бренд</dt>
                {/* Бренда может не быть, и это ЗАКОННОЕ состояние (`WORLD2` 3.4): под брендом
                    отдают своё, а свежая личность ещё ничего не отдавала. Пустая ячейка
                    заставила бы гадать, отвалилось ли что-то, поэтому пустое говорится
                    словами. Подставлять вместо него имя или прочерк нельзя: мир не выдумывает
                    за юзера (`WORLD2` 3.7, 0.1). */}
                <dd data-me-brand>
                  <Show
                    when={me().brand}
                    fallback={
                      <span data-state="empty">
                        бренда пока нет — под ним отдают своё, а отдавать ты ещё не начинал
                      </span>
                    }
                  >
                    {(brand) => brand()}
                  </Show>
                </dd>
                {/* Скоуп лежит ПО АДРЕСУ и раздаётся оттуда (`WORLD2` 3.4, `WORLD2-132`).
                    Прежней приписки «на ресурсе контроллера» больше нет и быть не может:
                    контроллер личности не держит — он времянка (`1.9`), и вопрос «на этом
                    ли он ресурсе» перестал существовать вместе с ответом «да». */}
                <dt>скоуп</dt>
                <dd data-me-scope>
                  <code>{me().scope.addr}</code> — раздаётся машиной{" "}
                  <code data-scope-host>{me().scope.host}</code>
                </dd>
                <Show when={me().since}>
                  {(since) => (
                    <>
                      <dt>вход длится с</dt>
                      <dd>
                        <time datetime={since()}>{since()}</time>
                      </dd>
                    </>
                  )}
                </Show>
              </dl>
            </>
          )}
        </Ожидание>
        <Show when={who()?.kind === "refusal"}>
          <Button onClick={() => props.onLeave()}>Войти заново</Button>
        </Show>
        <Выход control={props.control} onLeft={(сказано) => props.onLeave(сказано)} />
      </section>

      {/* ── поля ──────────────────────────────────────────────────────────── */}
      <section class="pult__block" data-block="fields">
        <Ожидание
          answer={fields()}
          отказ={(said) => (
            <RefusalView refusal={said} onWay={() => void askFields()} wayLabel="Спросить снова" />
          )}
        >
          {(list) => (
            <>
              <h2>
                Поля: <span data-count-fields>{list().length}</span>
              </h2>
              <Show
                when={list().length > 0}
                fallback={
                  <p data-state="empty">
                    Полей ещё нет — и это законное состояние, а не недоделка: пустой список
                    значит ровно то, что ни одного поля ты пока не заводил.
                  </p>
                }
              >
                <ul class="pult__list">
                  <For each={list()}>
                    {(field) => (
                      // Адрес и состояние поля показываются, только когда они есть: пока
                      // поле лишь записано в скоуп и никуда не поднято, они пусты — и
                      // пустая строка рядом с именем говорила бы, что что-то отвалилось.
                      <li class="pult__item" data-field={field.name}>
                        <span class="pult__name">{field.name}</span>
                        <Show when={field.addr}>
                          {(addr) => (
                            <code class="pult__aside" data-field-addr>
                              {addr()}
                            </code>
                          )}
                        </Show>
                        <Show when={field.state}>
                          {(состояние) => (
                            <span class="pult__aside" data-field-state>
                              — {состояние()}
                            </span>
                          )}
                        </Show>
                      </li>
                    )}
                  </For>
                </ul>
              </Show>
              <СозданиеПоля control={props.control} onDone={(answer) => setFields(answer)} />
            </>
          )}
        </Ожидание>
      </section>

      {/* ── источники ресурса ─────────────────────────────────────────────── */}
      <section class="pult__block" data-block="resources">
        <Ожидание
          answer={resources()}
          отказ={(said) => (
            <RefusalView
              refusal={said}
              onWay={() => void askResources()}
              wayLabel="Спросить снова"
            />
          )}
        >
          {(list) => (
            <>
              <h2>
                Источники ресурса: <span data-count-resources>{list().length}</span>
              </h2>
              {/* ПУСТО — ЗАКОННОЕ СОСТОЯНИЕ, и до ступени 2 его тут не бывало: в списке
                  всегда стоял ресурс «здесь», машина контроллера. Теперь список берётся из
                  СКОУПА (`WORLD2` 3.4), а машина контроллера юзеру не принадлежит — свежая
                  личность видит здесь ноль строк, и это не отказ и не недоделка. Пустое
                  место заставило бы гадать, отвалилось ли что-то, поэтому оно сказано
                  словами. */}
              <Show
                when={list().length > 0}
                fallback={
                  <p data-state="empty">
                    Источников ещё нет — и это законное состояние: свежий скоуп это имя и
                    пустота. Машина, на которой стоит контроллер, сюда не попадает: она не
                    твоя, а его, и он времянка. Нужна она как участок — заведи её ниже, как
                    всякую другую.
                  </p>
                }
              >
                <ul class="pult__list">
                  <For each={list()}>{(res) => <СтрокаРесурса res={res} />}</For>
                </ul>
              </Show>
              <ДобавлениеРесурса
                control={props.control}
                onDone={(answer) => setResources(answer)}
              />
            </>
          )}
        </Ожидание>
      </section>
    </div>
  );
}

/**
 * Строка списка источников. Ни памяти, ни ядер — только то, что измерено (`WORLD2` 2.5).
 *
 * Ресурс — МАШИНА, до которой дотянулись, а не «машина с дверью» (`WORLD2-131`): про сам
 * ресурс говорит `reach`, про то, что на нём стоит, — список вещей. Два вопроса, два поля, и
 * второй из первого не выводится.
 *
 * Ресурса «здесь» в списке больше нет (`WORLD2-132`): список приезжает из скоупа, а машина
 * контроллера в него не входит — он времянка, и своё хозяйство в личность юзера не
 * подмешивает. Вместе с «здесь» ушла и строка «адрес изнутри машины не известен»: у каждого
 * участка адрес есть, потому что его назвал юзер.
 */
function СтрокаРесурса(props: { res: Resource }) {
  return (
    <li class="pult__item" data-resource={props.res.name}>
      <span class="pult__name">{props.res.name}</span>
      <dl class="pult__facts">
        <dt>адрес</dt>
        <dd>
          <code>{props.res.addr}</code>
        </dd>
        <dt>ресурс</dt>
        {/* Слова контроллера про САМУ машину — «отвечает» либо «молчит». Своей приписки к ним
            пульт не дописывает: сказанного достаточно, а второе «что это значит» рядом с
            первым — это уже наш словарь того же самого. */}
        <dd data-reach>{props.res.reach}</dd>
        <dt>вещи</dt>
        <dd>
          <Вещи things={props.res.things} />
        </dd>
      </dl>
    </li>
  );
}

/**
 * Что стоит на ресурсе — тремя разными ответами, а не двумя.
 *
 * «Не спросили» (`null`) и «спросили, там пусто» (`[]`) на экране РАЗНЫЕ: схлопнуть их в одну
 * строку значит показать человеку знание, которого у нас нет (`WORLD2` 4.2). Молчащий ресурс
 * — не пустая машина.
 */
function Вещи(props: { things: Resource["things"] }) {
  return (
    <Switch>
      <Match when={props.things === null}>
        <span data-things="unknown">не спросили — ресурс не ответил</span>
      </Match>
      <Match when={props.things?.length === 0}>
        <span data-things="empty">спросили — ничего не поднято</span>
      </Match>
      <Match when={props.things}>
        {(список) => (
          <ul class="pult__things" data-things="list">
            <For each={список()}>
              {(вещь) => (
                // `data-alive` — ровно то, что сказал контроллер, и ничего сверх: у вещи без
                // HEALTHCHECK-а здесь «нет», потому что ответа не спросить, — а не «мертва»
                // и тем более не «здорова». Чем именно это оказалось, сказано словами рядом,
                // и слова эти контроллера, не наши.
                <li data-thing={вещь.name} data-alive={вещь.alive ? "yes" : "no"}>
                  <span class="pult__name">{вещь.name}</span>{" "}
                  <span class="pult__aside">— {вещь.state}</span>
                </li>
              )}
            </For>
          </ul>
        )}
      </Match>
    </Switch>
  );
}

/**
 * ВЫХОД — ручка, а не забытая метка (`WORLD2-132`, `DELETE /api/session`).
 *
 * Выход не трогает своего состояния: скоуп лежит там, где лежал, и открывается тем же адресом
 * и паролем. Снимаются ВРЕМЯНКИ КОНТРОЛЛЕРА — контексты докера, ключи, блоки в `config`. Без
 * этого следующий вошедший увидел бы чужие территории, и «личность» перестала бы что-то
 * значить: ровно это ступень 2 и убирает.
 *
 * Отсюда правило экрана: ОТКАЗ ВЫХОДА ОСТАВЛЯЕТ НА МЕСТЕ. Уйти на экран входа, не сняв
 * времянок, значило бы показать человеку «вышел» там, где он не вышел, — и передать чужой
 * личности хозяйство предыдущей.
 */
function Выход(props: { control: Control; onLeft: (сказано: string) => void }) {
  const [busy, setBusy] = createSignal(false);
  const [refusal, setRefusal] = createSignal<Refusal | undefined>();

  async function выйти() {
    if (busy()) return;
    setBusy(true);
    setRefusal(undefined);

    const answer = await props.control.leave();
    setBusy(false);

    if (answer.kind === "refusal") {
      setRefusal(answer.refusal);
      return;
    }
    // Слова контроллера о том, чего выход НЕ тронул, едут с нами на экран входа: человек,
    // не увидевший их, гадал бы, не стёрлась ли вместе с сессией и личность.
    props.onLeft(answer.value.note);
  }

  return (
    <div class="pult__row" data-action="leave">
      <Button onClick={() => void выйти()} disabled={busy()} aria-busy={busy() ? "true" : undefined}>
        {busy() ? "Выхожу…" : "Выйти"}
      </Button>
      <Show when={refusal()}>{(said) => <RefusalView refusal={said()} />}</Show>
    </div>
  );
}

function СозданиеПоля(props: {
  control: Control;
  onDone: (answer: Answer<WorldField[]>) => void;
}) {
  const [name, setName] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [refusal, setRefusal] = createSignal<Refusal | undefined>();
  const [note, setNote] = createSignal("");

  async function создать() {
    if (busy()) return;
    setBusy(true);
    setRefusal(undefined);

    const answer = await props.control.addField(name());
    setBusy(false);

    if (answer.kind === "refusal") {
      setRefusal(answer.refusal);
      return;
    }
    setName("");
    // Контроллер говорит вслух, чего НЕ произошло: поле записано, но нигде не поднято.
    // Проглотить эту строку значило бы оставить человека ждать поднятого поля.
    setNote(answer.value.note);
    props.onDone({ kind: "ok", value: answer.value.fields });
  }

  return (
    <form
      class="pult__form"
      onSubmit={(event) => {
        event.preventDefault();
        void создать();
      }}
    >
      <Field class="pult__field" value={name()} onChange={setName}>
        <Label>Создать поле</Label>
        <Input placeholder="дом" autocomplete="off" />
      </Field>
      <Button type="submit" disabled={busy()} aria-busy={busy() ? "true" : undefined}>
        Создать поле
      </Button>
      <Show when={note()}>{(said) => <p class="pult__note">{said()}</p>}</Show>
      <Show when={refusal()}>{(said) => <RefusalView refusal={said()} />}</Show>
    </form>
  );
}

function ДобавлениеРесурса(props: {
  control: Control;
  onDone: (answer: Answer<Resource[]>) => void;
}) {
  const [name, setName] = createSignal("");
  const [addr, setAddr] = createSignal("");
  const [creds, setCreds] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [refusal, setRefusal] = createSignal<Refusal | undefined>();

  async function добавить() {
    if (busy()) return;
    setBusy(true);
    setRefusal(undefined);

    const answer = await props.control.addResource({ name: name(), addr: addr(), creds: creds() });
    setBusy(false);

    if (answer.kind === "refusal") {
      setRefusal(answer.refusal);
      return;
    }
    setName("");
    setAddr("");
    setCreds("");
    // Список берём из ТОГО ЖЕ ответа: главное, ради чего человек жал кнопку, — увидеть, что
    // источников стало два (`WORLD2-80`).
    props.onDone({ kind: "ok", value: answer.value.resources });
  }

  return (
    <form
      class="pult__form"
      onSubmit={(event) => {
        event.preventDefault();
        void добавить();
      }}
    >
      <h3>Добавить ресурс</h3>
      <p class="pult__hint">
        На названной машине встанет дверь — этим она и включается в мир. Называешь ТРИ вещи:
        имя участка, адрес и креды. Имя даёшь ты, и в твоём скоупе оно не повторяется — на нём
        стоит адрес локации; из адреса машины мир его не выводит и молча не подставляет.
        Ключ уходит в твой скоуп и в связку контроллера рядом с адресом: своего
        хранилища ключей мир не заводит.
      </p>
      <Field class="pult__field" value={name()} onChange={setName}>
        <Label>Имя</Label>
        <Input placeholder="vps" autocomplete="off" />
      </Field>
      <Field class="pult__field" value={addr()} onChange={setAddr}>
        <Label>Адрес</Label>
        <Input placeholder="world@10.8.0.5" autocomplete="off" />
      </Field>
      <Field class="pult__field" value={creds()} onChange={setCreds}>
        <Label>Креды</Label>
        <Textarea rows={3} placeholder="ключ целиком" />
      </Field>
      <Button type="submit" disabled={busy()} aria-busy={busy() ? "true" : undefined}>
        {busy() ? "Ставлю дверь…" : "Добавить ресурс"}
      </Button>
      <Show when={refusal()}>{(said) => <RefusalView refusal={said()} />}</Show>
    </form>
  );
}

/**
 * Три состояния блока одним местом: спрашиваем · отказ · ответ.
 *
 * Порядок важен: `undefined` во время загрузки не должен случайно оказаться «пустым списком»
 * — это разные вещи, и путать их на экране дороже всего.
 */
function Ожидание<T>(props: {
  answer: Answer<T> | undefined;
  отказ: (said: Refusal) => import("solid-js").JSX.Element;
  // Значение приходит ФУНКЦИЕЙ, а не готовым: снятое здесь один раз, оно замёрзло бы, и
  // список после добавления ресурса остался бы прежним — ровно тем, ради чего человек и
  // нажимал кнопку (`WORLD2-80`).
  children: (value: () => T) => import("solid-js").JSX.Element;
}) {
  return (
    <Switch
      fallback={
        <p data-state="loading" class="pult__hint">
          Спрашиваю контроллер…
        </p>
      }
    >
      <Match when={props.answer?.kind === "refusal" ? props.answer.refusal : undefined}>
        {(said) => props.отказ(said())}
      </Match>
      <Match when={props.answer?.kind === "ok" ? props.answer : undefined}>
        {(ok) => props.children(() => ok().value)}
      </Match>
    </Switch>
  );
}
