// Экран 1 — ВХОД. Два поля: где лежит твой скоуп и пароль к нему.
//
// Больше на нём нет ничего — ни ресурсов, ни полей, ни имени: до входа их не существует
// (`WORLD2-102`). Это не аскетизм оформления, а честность: показать список чего-нибудь до
// того, как мир узнал, кто спрашивает, значило бы показать чужое или выдуманное.
//
// **Отдельной пары «личность плюс пароль» нет** (`WORLD2-77`, решение user): пароль — это ключ
// к АДРЕСУ, а не к личности. Дотянулся до скоупа — значит твой.
//
// **АДРЕС — ЭТО АДРЕС РАЗДАЧИ** (`WORLD2` 3.4, `WORLD2-132`): обычный HTTP-адрес, и состояние
// лежит в её корне. Пути на машине контроллера (`/scope/егор`) больше нет: он и означал
// личность, лежащую томом контроллера — снёс контроллер, потерял себя (`WORLD2-124`).
//
// **ХОДА «ЗАВЕСТИ ЗДЕСЬ» НЕ СУЩЕСТВУЕТ** (`WORLD2` 3.7, решение user 2026-08-16). Контроллер
// держателем чужой личности не становится — он времянка (`1.9`). Заведение это отдельная
// ручка и отдельный разговор: юзер называет АДРЕС, по которому его состояние будет лежать, и
// машину, на которой контроллер поднимет раздачу.
//
// **Пар при заведении ДВЕ, и путать их дорого** (`WORLD2` 3.4, «Два адреса»): машина — куда
// дотянуться и что поднять; скоуп — по какому адресу потом входить. Креды машины юзер даёт
// руками ровно один раз, потому что скоупа в этот момент ещё нет; дальше они лежат в нём. На
// слиянии этих двух пар выросла мёртвая `WORLD2-77`.
import { Show, createSignal } from "solid-js";

import type { Control, CreateScopeRequest, Refusal, Session } from "./control.js";
import { RefusalView } from "./Refusal.jsx";
import { Button, Field, Input, Label, Textarea } from "./ui.jsx";

export type SignInProps = {
  control: Control;
  /** Вход состоялся. Дальше экраном распоряжается пульт, а не эта форма. */
  onEntered: (session: Session) => void;
};

export function SignIn(props: SignInProps) {
  const [addr, setAddr] = createSignal("");
  const [password, setPassword] = createSignal("");
  const [name, setName] = createSignal("");
  const [brand, setBrand] = createSignal("");
  const [участок, setУчасток] = createSignal("");
  const [машина, setМашина] = createSignal("");
  const [креды, setКреды] = createSignal("");
  // Заведение — отдельный режим формы, а не догадка по пустому ответу: завести личность
  // молча, потому что «ничего не нашлось», значит однажды завести её на опечатке в адресе.
  const [creating, setCreating] = createSignal(false);
  // Поднимать ли раздачу. Юзер вправе поднять её сам — это его вилка, и мир в неё не смотрит
  // (`WORLD2` 0.3). Выбор человек делает ЯВНО: пустые поля машины — это не «машины нет», и
  // решать за него по пустоте значило бы снова угадывать.
  const [поднять, setПоднять] = createSignal(true);
  const [busy, setBusy] = createSignal(false);
  const [refusal, setRefusal] = createSignal<Refusal | undefined>();

  async function войти() {
    if (busy()) return;
    setBusy(true);
    setRefusal(undefined);

    // Вход — АДРЕС И ПАРОЛЬ, и больше ничего. Ни имени, ни бренда, ни «завести»: разница
    // между «состояние есть» и «состояния нет» только в исходе, а спрашивается одно и то же.
    const answer = await props.control.enter({ addr: addr(), password: password() });

    setBusy(false);
    if (answer.kind === "refusal") {
      setRefusal(answer.refusal);
      return;
    }
    props.onEntered(answer.value);
  }

  async function завести() {
    if (busy()) return;
    setBusy(true);
    setRefusal(undefined);

    const заявка: CreateScopeRequest = {
      scope: { addr: addr(), password: password() },
      identity: { name: name(), brand: brand() },
    };
    if (поднять()) {
      // Машина называется ЦЕЛИКОМ — три вещи (`WORLD2` 2.5 п. 11): имя участка, адрес и
      // креды. Имя не выводится из адреса и не подставляется молча: на нём стоит адрес
      // локации, и мир за юзера его не выдумывает (`WORLD2` 3.7).
      заявка.machine = { name: участок(), addr: машина(), creds: креды() };
    }
    const answer = await props.control.createScope(заявка);

    setBusy(false);
    if (answer.kind === "refusal") {
      setRefusal(answer.refusal);
      return;
    }
    props.onEntered(answer.value);
  }

  /** Раздача по адресу отвечает, а состояния в ней нет — контроллер сам назвал этот выход. */
  const noScope = () => refusal()?.code === "no-scope";

  /**
   * Выход из отказа `no-scope`: та же форма, тот же адрес и тот же пароль — набирать их
   * дважды человек не должен.
   *
   * Раздачу при этом НЕ поднимаем, и это не догадка пульта: `no-scope` значит ровно то, что
   * раздача по адресу ОТВЕЧАЕТ, а состояния в ней нет, — так сказал контроллер, и просить
   * его поднять её ещё раз он отказал бы кодом `share-already`. Выбор остаётся видимым и
   * переключаемым: сказанное показано, а не решено за человека.
   */
  function заводитьПоТомуЖеАдресу() {
    setПоднять(false);
    setCreating(true);
  }

  return (
    <section class="pult__block" data-screen="sign-in">
      <h2>Вход в свой скоуп</h2>
      <p>
        Скоуп — это твоя личность: имя, бренд, ключи, территории и поля. Он лежит ПО АДРЕСУ,
        там, где ты его положил, и раздаётся оттуда; контроллер до него дотягивается и ничего
        твоего у себя не держит. Входов в мир может быть сколько угодно и откуда угодно —
        этот вход один из них.
      </p>

      <form
        class="pult__form"
        onSubmit={(event) => {
          event.preventDefault();
          void (creating() ? завести() : войти());
        }}
      >
        <Field class="pult__field" value={addr()} onChange={setAddr}>
          <Label>Где лежит твой скоуп</Label>
          <Input placeholder="http://10.8.0.5:8070/" autocomplete="off" />
        </Field>

        <Field class="pult__field" value={password()} onChange={setPassword}>
          <Label>Пароль скоупа</Label>
          <Input type="password" autocomplete="off" />
        </Field>

        <Show when={creating()}>
          <Field class="pult__field" value={name()} onChange={setName}>
            <Label>Имя</Label>
            <Input placeholder="егор" autocomplete="off" />
          </Field>

          {/* Бренда может не быть, и это законное состояние (`WORLD2-135`): свежесозданный
              скоуп — это имя и пустота. Поле пустым и остаётся, и заведение на этом не
              спотыкается. */}
          <Field class="pult__field" value={brand()} onChange={setBrand}>
            <Label>Бренд</Label>
            <Input placeholder="можно не называть" autocomplete="off" />
          </Field>

          <div class="pult__row">
            <Button onClick={() => setПоднять(!поднять())}>
              {поднять()
                ? "Раздача уже стоит по этому адресу"
                : "Раздачи ещё нет — подними её на моей машине"}
            </Button>
          </div>

          <Show
            when={поднять()}
            fallback={
              <p class="pult__hint" data-machine="own">
                Машину не называем: раздача по этому адресу должна уже отвечать. Чем она
                поднята — не дело мира: отдаёт файл и принимает файл, хоть на ардуино.
              </p>
            }
          >
            <div data-machine="raise">
              <p class="pult__hint">
                Контроллер дотянется до этой машины и поднимет на ней раздачу. Это ВТОРАЯ
                пара, и с первой её путать дорого: адрес скоупа — то, чем ты потом входишь, а
                это — где он будет лежать. Креды нужны руками ровно сейчас, потому что скоупа
                ещё нет; дальше они будут лежать в нём.
              </p>
              <Field class="pult__field" value={участок()} onChange={setУчасток}>
                <Label>Имя участка</Label>
                <Input placeholder="vps" autocomplete="off" />
              </Field>
              <Field class="pult__field" value={машина()} onChange={setМашина}>
                <Label>Адрес машины</Label>
                <Input placeholder="world@10.8.0.5" autocomplete="off" />
              </Field>
              <Field class="pult__field" value={креды()} onChange={setКреды}>
                <Label>Креды к машине</Label>
                <Textarea rows={3} placeholder="ключ целиком" />
              </Field>
            </div>
          </Show>
        </Show>

        <div class="pult__row">
          <Button type="submit" disabled={busy()} aria-busy={busy() ? "true" : undefined}>
            {creating() ? "Завести скоуп" : "Войти"}
          </Button>
          <Button
            onClick={() => {
              setCreating(!creating());
              setRefusal(undefined);
            }}
          >
            {creating() ? "У меня уже есть скоуп" : "Скоупа ещё нет — завести по адресу"}
          </Button>
        </div>
      </form>

      <p class="pult__hint">
        Скоуп заводится ТАМ, где ты его назвал, — «завести здесь», на машине контроллера, мир
        не умеет и не будет: контроллер времянка, и личность, положенная в него, исчезла бы
        вместе с ним.
      </p>

      <Show when={refusal()}>
        {(said) => (
          <RefusalView
            refusal={said()}
            onWay={noScope() && !creating() ? заводитьПоТомуЖеАдресу : undefined}
            wayLabel="Завести скоуп по этому адресу"
          />
        )}
      </Show>
    </section>
  );
}
