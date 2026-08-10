// Разговор с дверью — ЕДИНСТВЕННОЕ место, где пульт узнаёт что-либо о поле.
//
// Пульт — это та же сборка, повёрнутая к человеку (`kb:WORLD-53`), а не второй мир на фронте.
// Отсюда главный инвариант файла:
//
//	ПУЛЬТ ПОКАЗЫВАЕТ ТО, ЧТО ОТДАЛА ДВЕРЬ, и не заводит своего состояния поля.
//
// Практически это значит: здесь нет кэша, нет «локация наверняка ещё жива», нет вычисленных
// полей. Есть разбор ответа и перевод неответа в ОТКАЗ, который называет причину и выход
// (`kb:WORLD-31`). Маршрут локации тоже не считается из имени, хотя правило известно: считать
// его здесь значило бы завести вторую истину о том, где локация достижима.
//
// Контракт ручки — `tasker:WORLD-31`, зона `core`, пакет `core/internal/door`:
//
//	GET /api/locations → 200 {"count": N, "locations": [{name, addr, gives, since, route}]}
//	отказ             → {"error": "<код>", "detail": "<причина и выход>"} с кодом состояния
//
// Пустой список — законное состояние поля, а не ошибка (`kb:WORLD-2`), и потому НЕ отказ.

/** Ручка реестра. Тот же путь, что `door.Prefix` в зоне `core`. */
export const LOCATIONS_PATH = "/api/locations";

/** Строка реестра — ровно то, что отдаёт дверь. Ни поля больше. */
export type Location = {
  /** имя локации; оно же её маршрут за дверью */
  name: string;
  /** адрес в общей сети, «имя:порт», без схемы */
  addr: string;
  /** что даёт — одной строкой */
  gives: string;
  /** когда зарегистрирована, RFC3339 */
  since: string;
  /** путь, по которому локация достижима снаружи */
  route: string;
};

/**
 * Отказ по канону (`kb:WORLD-31`): причина и выход, а не «что-то пошло не так».
 *
 * `exit` пуст ровно в одном случае — отказ произнесла САМА дверь (`from: "door"`): по канону
 * её `detail` уже содержит выход, и приписывать к нему свой значило бы сказать человеку два
 * разных «что делать». Всё, что пульт сочиняет сам, обязано нести выход — это стережёт проба.
 */
export type Refusal = {
  /** машинное имя отказа — попадает на экран рядом с текстом, чтобы о нём можно было спросить */
  code: string;
  /** что именно не получилось */
  reason: string;
  /** что сделать, чтобы отказ снялся; пусто — только когда выход назван внутри `reason` */
  exit: string;
  /** кто произнёс отказ: дверь или сам пульт */
  from: "door" | "panel";
};

/** Что пульт знает о поле прямо сейчас. Третьего состояния нет: либо поле, либо отказ. */
export type FieldView =
  | { kind: "field"; locations: Location[] }
  | { kind: "refusal"; refusal: Refusal };

/** Как ходить в сеть. Параметром — чтобы пробы вызывали каждый отказ нарочно (`kb:WORLD-32`). */
export type Fetcher = (input: string) => Promise<Response>;

const browserFetch: Fetcher = (input) => globalThis.fetch(input);

function refuse(code: string, reason: string, exit: string): FieldView {
  return { kind: "refusal", refusal: { code, reason, exit, from: "panel" } };
}

/**
 * Спросить дверь, кто сейчас в поле.
 *
 * Не бросает НИКОГДА: всякий неответ — это отказ на экране с причиной и выходом. Исключение,
 * улетевшее выше, показало бы человеку пустой экран либо красную полосу без объяснения, а это
 * ровно то, что канон называет провалом, а не мелочью.
 */
export async function readField(doFetch: Fetcher = browserFetch): Promise<FieldView> {
  let answer: Response;
  try {
    answer = await doFetch(LOCATIONS_PATH);
  } catch (cause) {
    return refuse(
      "door-unreachable",
      `дверь не отвечает по ${LOCATIONS_PATH}: ${describe(cause)}`,
      "подними сервис мира (из core/: `go run ./cmd/world`) и обнови страницу; " +
        "в деве запрос идёт через прокси vite — адрес двери задаётся переменной WORLD_DOOR, " +
        "по умолчанию http://127.0.0.1:8080",
    );
  }

  const body = await answer.text().catch(() => "");

  if (!answer.ok) {
    const said = readRefusal(body);
    if (said) {
      // Отказ, названный дверью. Пропускаем как есть: переписывать чужой отказ своими
      // словами значит потерять выход, который в нём уже назван.
      return { kind: "refusal", refusal: said };
    }
    return refuse(
      "door-not-answering",
      `на ${LOCATIONS_PATH} пришло ${answer.status} ${answer.statusText || "без пояснения"}` +
        (body.trim() ? `: ${cut(body)}` : ", тело пустое"),
      "это ответ не двери, а того, кто оказался на её месте: проверь, что на порту стоит " +
        "сервис мира, а прокси дева смотрит на него (`curl <адрес двери>/healthz`)",
    );
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch (cause) {
    return refuse(
      "answer-not-json",
      `дверь ответила 200, но тело не разбирается как JSON (${describe(cause)}): ${cut(body)}`,
      "чаще всего так отвечает статика вместо ручки — проверь, что запрос идёт на дверь, " +
        "а не на дев-сервер vite (`curl <адрес двери>" + LOCATIONS_PATH + "`)",
    );
  }

  return readLocations(parsed);
}

/** Разбор успешного ответа. Форму проверяем целиком: показывать половину строки нечестно. */
function readLocations(parsed: unknown): FieldView {
  const notField = (why: string) =>
    refuse(
      "answer-not-field",
      `ответ по ${LOCATIONS_PATH} не похож на список локаций: ${why}`,
      "ручка реестра отвечает {\"count\":N,\"locations\":[…]} (контракт — tasker:WORLD-31); " +
        "разошлась форма — это вопрос к зоне core, пульт её не додумывает",
    );

  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return notField("это не объект");
  }
  const raw = (parsed as { locations?: unknown }).locations;
  if (!Array.isArray(raw)) {
    return notField("нет массива «locations»");
  }

  const locations: Location[] = [];
  for (let i = 0; i < raw.length; i += 1) {
    const item = raw[i];
    if (item === null || typeof item !== "object" || Array.isArray(item)) {
      return notField(`элемент ${i} — не объект`);
    }
    const rec = item as Record<string, unknown>;
    // Порядок полей — порядок строк в отказе: человек читает первое недостающее.
    for (const field of ["name", "addr", "gives", "route"] as const) {
      if (typeof rec[field] !== "string" || rec[field] === "") {
        return notField(`у элемента ${i} нет строки «${field}»`);
      }
    }
    locations.push({
      name: rec.name as string,
      addr: rec.addr as string,
      gives: rec.gives as string,
      // `since` необязательно: дверь его отдаёт, но пульт без даты показать список может,
      // а вот соврать датой — нет. Нет — значит нет, пустым и покажем.
      since: typeof rec.since === "string" ? rec.since : "",
      route: rec.route as string,
    });
  }

  // Пустой массив доезжает сюда как пустое поле — и это НЕ отказ (`kb:WORLD-2`).
  return { kind: "field", locations };
}

/** Отказ, названный дверью: `{"error":…,"detail":…}`. Не та форма — не наш случай. */
function readRefusal(body: string): Refusal | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return null;
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return null;
  }
  const rec = parsed as Record<string, unknown>;
  if (typeof rec.error !== "string" || typeof rec.detail !== "string") {
    return null;
  }
  return { code: rec.error, reason: rec.detail, exit: "", from: "door" };
}

function describe(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause);
}

/** Чужое тело в отказ кладём куском: страница отказа не место для мегабайта HTML. */
function cut(body: string): string {
  const flat = body.replace(/\s+/g, " ").trim();
  return flat.length > 200 ? `${flat.slice(0, 200)}…` : flat;
}
