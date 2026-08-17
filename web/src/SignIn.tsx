// Экран 1 — ВХОД и ЗАВЕДЕНИЕ. ДВЕ ДВЕРИ, видные сразу, а не переключатель внизу формы.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ НАЙДЕНО ЖИВЫМ ПРОГОНОМ (`WORLD2-142`): первый юзер мира не нашёл, как завести        │
// │ личность. Он открыл пульт, заполнил единственную видимую форму — форму ВХОДА — и     │
// │ получил `no-address`. Отказ был верный, а человек даже не знал, что он не в той      │
// │ форме: заведение пряталось строкой-переключателем ВНИЗУ.                             │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Довод не вкусовой, он из канона. `WORLD2` 3.4, «Два адреса, и путать их дорого»: при
// заведении юзер называет ДВЕ разные пары, при входе — ОДНУ. Это разные разговоры разной
// длины, и одна форма с переключателем подталкивает считать их одним. Живой прогон это и
// показал: человек заполнил поля машины там, где спрашивали адрес скоупа. На слиянии этих
// двух пар выросла мёртвая `WORLD2-77` — канон предупреждал заранее.
//
// Поэтому здесь вкладки, и выбор виден ДО заполнения:
//
//	«Войти»        — одна пара: адрес раздачи и пароль. И больше ничего;
//	«Завести скоуп» — две пары плюс личность: скоуп (адрес и пароль) · машина (имя, адрес, креды).
//
// **Хода «завести здесь» не существует** (`WORLD2` 3.7): контроллер держателем чужой личности
// не становится — он времянка (`1.9`). Заведение называет АДРЕС, по которому состояние будет
// лежать, и — если раздачи там ещё нет — машину, где контроллер её поднимет.
//
// **Отдельной пары «личность плюс пароль» нет** (`WORLD2-77`): пароль — ключ к АДРЕСУ, а не к
// личности. Дотянулся до скоупа — значит твой.
import { Show, createSignal } from "solid-js";

import type { Control, Creds, CreateScopeRequest, Refusal, Session } from "./control.js";
import { CredsField, пустыеКреды } from "./Creds.jsx";
import { RefusalView } from "./Refusal.jsx";
import { Button, Field, Input, Label } from "./ui.jsx";

export type SignInProps = {
  control: Control;
  /** Вход состоялся. Дальше экраном распоряжается пульт, а не эта форма. */
  onEntered: (session: Session) => void;
};

/** Какая дверь открыта. Не «режим формы», а именно дверь: разговоры за ними разные. */
type Дверь = "enter" | "create";

export function SignIn(props: SignInProps) {
  // Пара СКОУПА — общая у обеих дверей, и это не экономия полей: адрес и пароль там и там
  // значат одно и то же, а набирать их дважды человек не должен (`WORLD2-142`).
  const [addr, setAddr] = createSignal("");
  const [password, setPassword] = createSignal("");
  const [name, setName] = createSignal("");
  const [brand, setBrand] = createSignal("");
  // Пара МАШИНЫ — только у заведения, и ни одно её поле не живёт в форме входа. Именно их
  // человек однажды и заполнил не там.
  const [участок, setУчасток] = createSignal("");
  const [машина, setМашина] = createSignal("");
  const [creds, setCreds] = createSignal<Creds>(пустыеКреды());
  const [дверь, setДверь] = createSignal<Дверь>("enter");
  // Поднимать ли раздачу. Юзер вправе поднять её сам — это его вилка, и мир в неё не смотрит
  // (`WORLD2` 0.3). Выбор человек делает ЯВНО: пустые поля машины — это не «машины нет», и
  // решать за него по пустоте значило бы снова угадывать.
  const [поднять, setПоднять] = createSignal(true);
  const [busy, setBusy] = createSignal(false);
  const [refusal, setRefusal] = createSignal<Refusal | undefined>();

  /** Переход между дверями руками: новый разговор — чистый отказ. */
  function открыть(куда: Дверь) {
    setДверь(куда);
    setRefusal(undefined);
  }

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
      заявка.machine = { name: участок(), addr: машина(), creds: creds() };
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
   * ВЫХОД, ИСПОЛНИМЫЙ ТАМ, ГДЕ ОТКАЗ УВИДЕН (`WORLD2-142`).
   *
   * Контроллер называет выходом `POST /api/scope` — и это правильный ответ машине, но человеку
   * в браузере он говорит языком консоли. Причину и выходы его мы оставляем ДОСЛОВНО (`2.3`),
   * а рядом даём кнопку, которая делает РОВНО НАЗВАННОЕ: открывает дверь заведения с тем же
   * адресом и паролем. Это не второй ответ человеку, а тот же самый, исполнимый.
   *
   * Раздачу при этом не поднимаем, и это не догадка: `no-scope` значит, что раздача по адресу
   * ОТВЕЧАЕТ, а состояния в ней нет, — так сказал контроллер, и просьбу поднять её ещё раз он
   * отказал бы кодом `share-already`. Выбор остаётся видимым и переключаемым.
   *
   * Отказ при этом НЕ гасится: человек перешёл не сам, а по выходу, и причина должна остаться
   * у него перед глазами.
   */
  function заводитьПоТомуЖеАдресу() {
    setПоднять(false);
    setДверь("create");
  }

  return (
    <section class="pult__block" data-screen="sign-in">
      <h2>Твой скоуп</h2>
      <p>
        Скоуп — это твоя личность: имя, бренд, ключи, территории и поля. Он лежит ПО АДРЕСУ,
        там, где ты его положил, и раздаётся оттуда; контроллер до него дотягивается и ничего
        твоего у себя не держит. Входов в мир может быть сколько угодно и откуда угодно —
        этот вход один из них.
      </p>

      {/* ДВЕ ДВЕРИ, и обе видны до заполнения. Пряталась вторая — человек не находил её
          вовсе (`WORLD2-142`), а находил отказ. */}
      <div class="pult__doors" role="tablist" aria-label="вход и заведение">
        <Button
          role="tab"
          data-door="enter"
          aria-selected={дверь() === "enter" ? "true" : "false"}
          onClick={() => открыть("enter")}
        >
          Войти
        </Button>
        <Button
          role="tab"
          data-door="create"
          aria-selected={дверь() === "create" ? "true" : "false"}
          onClick={() => открыть("create")}
        >
          Завести скоуп
        </Button>
      </div>

      <Show
        when={дверь() === "create"}
        fallback={
          <form
            class="pult__form"
            role="tabpanel"
            data-panel="enter"
            onSubmit={(event) => {
              event.preventDefault();
              void войти();
            }}
          >
            <p class="pult__hint">Одна пара: где лежит скоуп и пароль к нему.</p>
            <ПараСкоупа
              addr={addr()}
              password={password()}
              onAddr={setAddr}
              onPassword={setPassword}
            />
            <Button type="submit" disabled={busy()} aria-busy={busy() ? "true" : undefined}>
              Войти
            </Button>
          </form>
        }
      >
        <form
          class="pult__form"
          role="tabpanel"
          data-panel="create"
          onSubmit={(event) => {
            event.preventDefault();
            void завести();
          }}
        >
          <p class="pult__hint">
            Две пары, и путать их дорого. Первая — САМ СКОУП: адрес, по которому он будет
            лежать, и пароль, которым ты потом входишь. Вторая — МАШИНА, до которой контроллер
            дотянется, чтобы поднять там раздачу.
          </p>

          <h3>Скоуп</h3>
          <ПараСкоупа
            addr={addr()}
            password={password()}
            onAddr={setAddr}
            onPassword={setPassword}
          />

          <h3>Личность</h3>
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

          <h3>Машина</h3>
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
                Контроллер дотянется до этой машины и поднимет на ней раздачу. Креды нужны
                руками ровно сейчас, потому что скоупа ещё нет; дальше они будут лежать в нём.
              </p>
              <Field class="pult__field" value={участок()} onChange={setУчасток}>
                <Label>Имя участка</Label>
                <Input placeholder="vps" autocomplete="off" />
              </Field>
              <Field class="pult__field" value={машина()} onChange={setМашина}>
                <Label>Адрес машины</Label>
                <Input placeholder="world@10.8.0.5" autocomplete="off" />
              </Field>
              <CredsField creds={creds()} onChange={setCreds} />
            </div>
          </Show>

          <Button type="submit" disabled={busy()} aria-busy={busy() ? "true" : undefined}>
            Завести скоуп
          </Button>
        </form>
      </Show>

      <p class="pult__hint">
        Скоуп заводится ТАМ, где ты его назвал, — «завести здесь», на машине контроллера, мир
        не умеет и не будет: контроллер времянка, и личность, положенная в него, исчезла бы
        вместе с ним.
      </p>

      <Show when={refusal()}>
        {(said) => (
          <RefusalView
            refusal={said()}
            onWay={noScope() && дверь() === "enter" ? заводитьПоТомуЖеАдресу : undefined}
            wayLabel="Завести скоуп по этому адресу"
          />
        )}
      </Show>
    </section>
  );
}

/**
 * Пара скоупа — адрес и пароль. Одним куском на обе двери: слова у них там и там одни, и
 * разъехавшись, они начали бы означать разное.
 */
function ПараСкоупа(props: {
  addr: string;
  password: string;
  onAddr: (v: string) => void;
  onPassword: (v: string) => void;
}) {
  return (
    <>
      <Field class="pult__field" value={props.addr} onChange={props.onAddr}>
        <Label>Где лежит твой скоуп</Label>
        <Input placeholder="http://10.8.0.5:8070/" autocomplete="off" />
      </Field>
      <Field class="pult__field" value={props.password} onChange={props.onPassword}>
        <Label>Пароль скоупа</Label>
        <Input type="password" autocomplete="off" />
      </Field>
    </>
  );
}
