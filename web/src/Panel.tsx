// Пульт мира. Два экрана, и пока только они (`WORLD2-102`): вход и мир.
//
// Пульт переехал от двери к КОНТРОЛЛЕРУ (`WORLD2` 3.7): дверь — вход и выход, она ничего не
// показывает и ничем не распоряжается; лицо для человека живёт при контроллере. Экран «кто
// сейчас в поле» ушёл вместе с этим переездом — он спрашивал дверь, а пульт с ней больше не
// разговаривает.
//
// Что осталось неизменным от первой версии и не обсуждается по ходу:
//
//	Всё, что можно из консоли, можно из пульта; веб привилегий не получает и своей логики
//	не заводит.
//
// Практически: пульт не делает ничего, чего не делает ручка контроллера, не держит своей
// модели мира и не досчитывает того, чего ему не сказали.
import { Show, createSignal } from "solid-js";

import { type Control, type Session, liveControl } from "./control.js";
import { SignIn } from "./SignIn.jsx";
import { World } from "./World.jsx";

export type PanelProps = {
  /**
   * С чем разговаривать. Подменяется пробой, чтобы каждый отказ вызывался НАРОЧНО
   * (`WORLD2` 4.2: проба, которую ни разу не заставили упасть, пробой не является).
   */
  control?: Control;
};

export function Panel(props: PanelProps) {
  // Собеседник снимается один раз на монтаже: контроллер у пульта не меняется по ходу жизни
  // страницы — меняется мир, а не тот, кто о нём рассказывает.
  const control = props.control ?? liveControl();
  const [session, setSession] = createSignal<Session | undefined>();

  return (
    <main class="pult">
      <header class="pult__head">
        <h1>Пульт мира</h1>
        <p class="pult__sub">
          <Show
            when={session()}
            fallback="Лицо мира стоит при контроллере: он отдаёт данные и делает действия, пульт их показывает. Пока не вошёл — показывать нечего."
          >
            Всё, что видно здесь, пульт спросил у контроллера и показывает как есть: своего
            состояния мира он не держит и ничего не досчитывает.
          </Show>
        </p>
      </header>

      <Show
        when={session()}
        fallback={<SignIn control={control} onEntered={(entered) => setSession(entered)} />}
      >
        {(entered) => (
          <>
            <Show when={entered().created}>
              <p class="pult__note" data-state="created">
                Скоуп заведён здесь, на ресурсе контроллера. Дальше он твой: перенести его на
                другой ресурс можно будет позже, а входить в него — откуда угодно.
              </p>
            </Show>
            <World control={control} onLeave={() => setSession(undefined)} />
          </>
        )}
      </Show>

      <footer class="pult__foot">
        источник — контроллер: <code>/api/session</code>, <code>/api/me</code>,{" "}
        <code>/api/resources</code>, <code>/api/fields</code>
      </footer>
    </main>
  );
}
