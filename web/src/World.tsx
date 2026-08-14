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
import { Button, Field, Input, Label, Textarea } from "@omnifield/probe-web-ui";

import type { Answer, Control, Field as WorldField, Refusal, Resource } from "./control.js";
import { RefusalView } from "./Refusal.jsx";

export type WorldProps = {
  control: Control;
  /** Вернуться на экран входа. Нужен, когда сессии контроллера не стало: он живёт в памяти. */
  onLeave: () => void;
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
                <dd data-me-brand>{me().brand}</dd>
                <dt>скоуп</dt>
                <dd>
                  <code>{me().scope.addr}</code>{" "}
                  {me().scope.here ? "— на ресурсе контроллера" : "— на другом ресурсе, по связи"}
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
                      <li class="pult__item" data-field={field.name}>
                        <span class="pult__name">{field.name}</span>
                        <Show when={field.created}>
                          {(created) => (
                            <time class="pult__aside" datetime={created()}>
                              {created()}
                            </time>
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
              <ul class="pult__list">
                <For each={list()}>{(res) => <СтрокаРесурса res={res} />}</For>
              </ul>
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

/** Строка списка источников. Ни памяти, ни ядер — только то, что измерено (`WORLD2` 2.5). */
function СтрокаРесурса(props: { res: Resource }) {
  return (
    <li class="pult__item" data-resource={props.res.name}>
      <span class="pult__name">
        {props.res.name}
        <Show when={props.res.here}>
          {" "}
          <span class="pult__aside">— здесь стоит контроллер</span>
        </Show>
      </span>
      <dl class="pult__facts">
        <dt>адрес</dt>
        <dd>
          {/* Адреса у «здесь» нет намеренно: изнутри машины её собственный адрес неизвестен,
              а подставленный «localhost» стал бы ложью для того, кто смотрит снаружи. */}
          <Show when={props.res.addr} fallback={<span>изнутри машины не известен</span>}>
            {(addr) => <code>{addr()}</code>}
          </Show>
        </dd>
        <dt>дверь</dt>
        <dd data-door={props.res.alive ? "alive" : "dead"}>
          {props.res.door}
          {props.res.alive ? " — ресурс в мире" : " — в мир ещё не включён"}
        </dd>
      </dl>
    </li>
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
        На названной машине встанет дверь — этим она и включается в мир. Ресурс добавляется по
        адресу и кредам: своего хранилища ключей мир не заводит, ключ уходит в связку
        контроллера рядом с адресом.
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
