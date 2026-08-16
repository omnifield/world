// Экран 1 — ВХОД. Два поля: где лежит твой скоуп и креды к нему.
//
// Больше на нём нет ничего — ни ресурсов, ни полей, ни имени: до входа их не существует
// (`WORLD2-102`). Это не аскетизм оформления, а честность: показать список чего-нибудь до
// того, как мир узнал, кто спрашивает, значило бы показать чужое или выдуманное.
//
// **Отдельной пары «личность плюс пароль» нет** (`WORLD2-77`, решение user): креды — это ключ
// к АДРЕСУ, а не к личности. Дотянулся до скоупа — значит твой. Поэтому второе поле называется
// «креды к ресурсу», а не «пароль».
//
// **Скоупа нет вовсе — путь «завести здесь»**: личность разворачивается на том ресурсе, где
// стоит контроллер. Путь этот виден с самого начала, а не только после отказа: человек, у
// которого скоупа ещё нет, не должен для начала получить «нет» — но если отказ `no-scope`
// всё же пришёл, форма заведения открывается с тем же адресом, чтобы не набирать его дважды.
import { Show, createSignal } from "solid-js";

import type { Control, Refusal, Session } from "./control.js";
import { RefusalView } from "./Refusal.jsx";
import { Button, Field, Input, Label, Textarea } from "./ui.jsx";

export type SignInProps = {
  control: Control;
  /** Вход состоялся. Дальше экраном распоряжается пульт, а не эта форма. */
  onEntered: (session: Session) => void;
};

export function SignIn(props: SignInProps) {
  const [addr, setAddr] = createSignal("");
  const [creds, setCreds] = createSignal("");
  const [name, setName] = createSignal("");
  const [brand, setBrand] = createSignal("");
  // «Завести здесь» — отдельный режим формы, а не догадка по пустому ответу: завести личность
  // молча, потому что «ничего не нашлось», значит однажды завести её на опечатке в адресе.
  const [creating, setCreating] = createSignal(false);
  const [busy, setBusy] = createSignal(false);
  const [refusal, setRefusal] = createSignal<Refusal | undefined>();

  async function войти() {
    if (busy()) return;
    setBusy(true);
    setRefusal(undefined);

    const answer = await props.control.enter(
      creating()
        ? { addr: addr(), creds: creds(), create: true, name: name(), brand: brand() }
        : { addr: addr(), creds: creds() },
    );

    setBusy(false);
    if (answer.kind === "refusal") {
      setRefusal(answer.refusal);
      return;
    }
    props.onEntered(answer.value);
  }

  /** Скоупа по адресу нет — контроллер сам назвал этот выход, пульт лишь открывает форму. */
  const noScope = () => refusal()?.code === "no-scope";

  return (
    <section class="pult__block" data-screen="sign-in">
      <h2>Вход в свой скоуп</h2>
      <p>
        Скоуп — это твоя личность: имя, бренд и список полей. Он лежит в ОДНОМ месте, а входов
        в мир может быть сколько угодно и откуда угодно — этот вход один из них.
      </p>

      <form
        class="pult__form"
        onSubmit={(event) => {
          event.preventDefault();
          void войти();
        }}
      >
        <Field class="pult__field" value={addr()} onChange={setAddr}>
          <Label>Где лежит твой скоуп</Label>
          <Input placeholder="/scope/егор или world@10.8.0.5:/srv/scope" autocomplete="off" />
        </Field>

        <Field class="pult__field" value={creds()} onChange={setCreds}>
          <Label>Креды к ресурсу</Label>
          <Textarea rows={3} placeholder="ключ целиком; скоуп на этой машине — оставь пустым" />
        </Field>

        <Show when={creating()}>
          <Field class="pult__field" value={name()} onChange={setName}>
            <Label>Имя</Label>
            <Input placeholder="егор" autocomplete="off" />
          </Field>

          <Field class="pult__field" value={brand()} onChange={setBrand}>
            <Label>Бренд</Label>
            <Input placeholder="омнифилд" autocomplete="off" />
          </Field>
        </Show>

        <div class="pult__row">
          <Button type="submit" disabled={busy()} aria-busy={busy() ? "true" : undefined}>
            {creating() ? "Завести здесь" : "Войти"}
          </Button>
          <Button
            type="button"
            onClick={() => {
              setCreating(!creating());
              setRefusal(undefined);
            }}
          >
            {creating() ? "У меня уже есть скоуп" : "Скоупа нет — завести здесь"}
          </Button>
        </div>
      </form>

      <p class="pult__hint">
        Заводится скоуп только на ресурсе, где стоит контроллер: «завести» и «развернуть у
        соседа» — разные действия, и второе делается подъёмом, а не этой формой.
      </p>

      <Show when={refusal()}>
        {(said) => (
          <RefusalView
            refusal={said()}
            onWay={noScope() && !creating() ? () => setCreating(true) : undefined}
            wayLabel="Завести скоуп здесь"
          />
        )}
      </Show>
    </section>
  );
}
