// Разговор с КОНТРОЛЛЕРОМ — единственное место, где пульт узнаёт что-либо о мире.
//
// Пульт переехал от двери к контроллеру (`WORLD2` 3.7, задача `WORLD2-102`): дверь — вход и
// выход, она ничего не показывает и ничем не распоряжается; лицо для человека живёт при
// контроллере. Поэтому здесь нет ни одной ручки двери, и `/api/locations` из этой зоны ушёл
// вместе с экраном, который его показывал.
//
// Инвариант файла остался прежним и от переезда не зависит:
//
//	ПУЛЬТ ПОКАЗЫВАЕТ ТО, ЧТО ОТДАЛ КОНТРОЛЛЕР, и своего состояния мира не держит.
//
// Ни кэша, ни досчитанных полей, ни «ресурс наверняка ещё жив». Есть разбор ответа и перевод
// неответа в ОТКАЗ, который называет причину и выходы (`WORLD2` 2.3).
//
// Контракт — таблица из `WORLD2-102`, она же `control/README.md`, пакет `control/internal/api`:
//
//	POST   /api/session            вход: адрес скоупа и креды (или create — завести здесь)
//	GET    /api/me                 кто я сейчас
//	GET    /api/resources          источники ресурса: имя, адрес, жив ли
//	POST   /api/resources          добавить ресурс — на нём встаёт дверь
//	GET    /api/fields             поля юзера
//	POST   /api/fields             завести поле
//
// `DELETE /api/resources/{имя}` у контроллера есть, а здесь его нет намеренно: снятие ресурса
// — действие необратимое на чужой машине, и `WORLD2-102` его на экран не просит. Появится
// оно вместе с тем, кто решит, как человек это подтверждает, а не тихой кнопкой рядом со
// списком.

/** Ручки контроллера. Те же пути, что в `control/internal/api`. */
export const PATH = {
  session: "/api/session",
  me: "/api/me",
  resources: "/api/resources",
  fields: "/api/fields",
} as const;

/**
 * Отказ по канону (`WORLD2` 2.3): код машине, причина и выходы человеку. Пустое «не
 * получилось» — провал самого мира: юзеру нечего чинить.
 *
 * `ways` — массив, а не строка, потому что выходов почти всегда несколько, и склеенные в
 * абзац они перестают быть выходами. Всё, что сочиняет сам пульт, обязано нести хотя бы один
 * выход — это стережёт отдельная проба, а не договорённость.
 */
export type Refusal = {
  /** машинное имя отказа — попадает на экран рядом с текстом, чтобы о нём можно было спросить */
  code: string;
  /** что именно не получилось */
  why: string;
  /** что сделать, чтобы отказ снялся */
  ways: string[];
  /** кто произнёс отказ: сам пульт или контроллер */
  said: "panel" | "control";
  /**
   * Чей это код, если контроллер довёз его от соседнего инструмента (`deploy/remote.sh`).
   * Контроллер чужие коды не переводит в свои, и пульт тоже не переводит: человеку показывают
   * ровно тот код, по которому он найдёт причину.
   */
  from?: string;
};

/** Ответ ручки: либо значение, либо отказ. Исключений отсюда не вылетает — см. `call`. */
export type Answer<T> = { kind: "ok"; value: T } | { kind: "refusal"; refusal: Refusal };

/** Где лежит скоуп — так, как это увидел контроллер. */
export type ScopeView = {
  /** адрес, названный человеком: `/scope/егор` либо `world@10.8.0.5:/srv/scope` */
  addr: string;
  /** скоуп лежит на ресурсе контроллера */
  here: boolean;
  /** путь до скоупа на том ресурсе */
  path: string;
};

/** Личность юзера — ровно то, что назвал канон (`WORLD2` 3.4): имя и бренд. */
export type Identity = {
  name: string;
  brand: string;
  scope: ScopeView;
  /** с какого времени длится вход; у ответа входа его нет */
  since: string;
};

/** Вход состоялся. `created` — скоуп завели прямо сейчас, а не нашли. */
export type Session = Identity & { created: boolean; token: string };

/** Источник ресурса глазами юзера. Памяти и ядер здесь нет намеренно (`WORLD2` 2.5). */
export type Resource = {
  name: string;
  /** адрес; у ресурса контроллера пуст — изнутри машины её собственный адрес неизвестен */
  addr: string;
  /** на этом ресурсе стоит контроллер */
  here: boolean;
  /** отвечает ли ДВЕРЬ: вход и есть то, чем ресурс включается в мир (`WORLD2` 3.5) */
  alive: boolean;
  /** что видно про дверь словами: «здорова», «поднимается», «нет», «ресурс молчит» */
  door: string;
};

/** Поле юзера. Пока это ЗАПИСЬ о поле, а не поднятое поле — так говорит и сам контроллер. */
export type Field = { name: string; created: string };

/** Чем входят: адрес скоупа и креды к нему; `create` — «скоупа нет, заведи здесь». */
export type EnterRequest = {
  addr: string;
  creds?: string;
  create?: boolean;
  name?: string;
  brand?: string;
};

/** Чем добавляют ресурс: имя, адрес машины и ключ к ней. */
export type AddResourceRequest = { name: string; addr: string; creds?: string };

/**
 * Всё, что пульт умеет спросить у мира. Интерфейсом, а не набором функций, ради одного:
 * пробы подставляют его целиком и вызывают КАЖДЫЙ отказ нарочно (`WORLD2` 4.2 — проба,
 * которую ни разу не заставили упасть, пробой не является).
 */
export type Control = {
  enter(req: EnterRequest): Promise<Answer<Session>>;
  me(): Promise<Answer<Identity>>;
  resources(): Promise<Answer<Resource[]>>;
  addResource(req: AddResourceRequest): Promise<Answer<{ added: Resource; resources: Resource[] }>>;
  fields(): Promise<Answer<Field[]>>;
  addField(name: string): Promise<Answer<{ added: Field; fields: Field[]; note: string }>>;
};

/** Как ходить в сеть. Параметром — по той же причине, что и `Control`. */
export type Fetcher = (input: string, init?: RequestInit) => Promise<Response>;

const browserFetch: Fetcher = (input, init) => globalThis.fetch(input, init);

/** Куда писать трассу. Параметром — чтобы проба читала её, а не консоль раннера. */
export type Logf = (line: string) => void;

const browserLog: Logf = (line) => console.debug(line);

/** Часы замера. `performance` есть не везде, а трасса без времени — не трасса. */
function сейчас(): number {
  return typeof performance !== "undefined" ? performance.now() : Date.now();
}

/**
 * Живой контроллер.
 *
 * Метку сессии держим в памяти страницы и шлём заголовком `Authorization: Bearer`. Печенье
 * контроллер тоже ставит, но полагаться только на него нельзя: пульт может раздаваться не с
 * того origin, где стоит контроллер, — этот шов ещё не решён (вопрос зоны `control` к
 * architect в `WORLD2-101`). Заголовок работает в обоих случаях.
 *
 * В хранилище браузера метка НЕ кладётся: сессия контроллера живёт в памяти его процесса,
 * и пережившая перезагрузку страницы метка означала бы «вошёл», когда входа уже нет.
 */
export function liveControl(doFetch: Fetcher = browserFetch, logf: Logf = browserLog): Control {
  let token = "";

  const ask = <T>(
    path: string,
    read: (parsed: unknown, path: string) => Answer<T>,
    body?: unknown,
  ) => call(doFetch, path, token, read, body, logf);

  return {
    async enter(req) {
      const answer = await ask(PATH.session, readSession, req);
      if (answer.kind === "ok") {
        token = answer.value.token;
      }
      return answer;
    },
    me: () => ask(PATH.me, readIdentity),
    resources: () => ask(PATH.resources, readResources),
    // Список приезжает ТЕМ ЖЕ ответом — второй раз не спрашиваем: главное, ради чего человек
    // жал кнопку, — увидеть, что источников стало два (`WORLD2-80`), и лишний запрос между
    // действием и этим видом добавляет только ещё одну возможность не ответить.
    addResource: (req) => ask(PATH.resources, readAddedResource, req),
    fields: () => ask(PATH.fields, readFields),
    addField: (name) => ask(PATH.fields, readAddedField, { name }),
  };
}

/**
 * Один поход в контроллер: тело, отказ, разбор формы, трасса.
 *
 * Не бросает НИКОГДА: всякий неответ — это отказ на экране с причиной и выходом. Исключение,
 * улетевшее выше, показало бы человеку пустой экран либо красную полосу без объяснения.
 *
 * Строка трассы пишется ВСЕГДА, в том числе на отказе: «не получилось» без следа — это разбор
 * по памяти. Формат держится тем же, что у контроллера (`control: … http=… refusal=… dur=…`),
 * чтобы две трассы читались рядом одним взглядом.
 */
async function call<T>(
  doFetch: Fetcher,
  path: string,
  token: string,
  read: (parsed: unknown, path: string) => Answer<T>,
  body: unknown,
  logf: Logf,
): Promise<Answer<T>> {
  const начало = сейчас();
  const итог = (answer: Answer<T>, http: number | string): Answer<T> => {
    const код = answer.kind === "refusal" ? answer.refusal.code : "-";
    logf(
      `пульт: ${body === undefined ? "GET" : "POST"} ${path} http=${http} ` +
        `refusal=${код} dur=${(сейчас() - начало).toFixed(1)}ms`,
    );
    return answer;
  };

  const init: RequestInit = { method: body === undefined ? "GET" : "POST" };
  const headers: Record<string, string> = {};
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  init.headers = headers;

  let answer: Response;
  try {
    answer = await doFetch(path, init);
  } catch (cause) {
    return итог(
      refuse(
        "control-unreachable",
        `контроллер не отвечает по ${path}: ${describe(cause)}`,
        "подними контроллер: `./control/up.sh`, состояние — `./control/up.sh status`",
        "в деве запрос идёт через прокси vite — адрес контроллера задаётся переменной " +
          "WORLD_CONTROL, по умолчанию http://127.0.0.1:8090",
      ),
      "нет ответа",
    );
  }

  const text = await answer.text().catch(() => "");

  if (!answer.ok) {
    const said = readRefusal(text);
    if (said) {
      // Отказ, названный контроллером. Пропускаем как есть: переписывать чужой отказ своими
      // словами значит потерять выходы, которые в нём уже названы, и сказать человеку два
      // разных «что делать».
      return итог({ kind: "refusal", refusal: said }, answer.status);
    }
    return итог(
      refuse(
        "control-not-answering",
        `на ${path} пришло ${answer.status} ${answer.statusText || "без пояснения"}` +
          (text.trim() ? `: ${cut(text)}` : ", тело пустое"),
        "это ответ не контроллера, а того, кто оказался на его месте: проверь, что на порту " +
          "стоит он (`curl <адрес контроллера>/api/me`), а прокси дева смотрит туда же",
      ),
      answer.status,
    );
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (cause) {
    return итог(
      refuse(
        "answer-not-json",
        `контроллер ответил ${answer.status}, но тело не разбирается как JSON ` +
          `(${describe(cause)}): ${cut(text)}`,
        "чаще всего так отвечает статика вместо ручки — проверь, что запрос идёт на контроллер, " +
          "а не на дев-сервер vite (`curl <адрес контроллера>" + path + "`)",
      ),
      answer.status,
    );
  }

  return итог(read(parsed, path), answer.status);
}

// ── разбор ответов ───────────────────────────────────────────────────────────
//
// Форму проверяем целиком и до показа: показать половину ответа честнее нельзя, а
// додумывать недостающее поле — это заводить в пульте вторую истину о мире.

function readSession(parsed: unknown, path: string): Answer<Session> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");

  for (const field of ["name", "brand", "token"] as const) {
    if (!isFilled(rec[field])) return notExpected(path, `нет строки «${field}»`);
  }
  const scope = readScope(rec.scope);
  if (!scope) return notExpected(path, "нет объекта «scope» с полями addr, here, path");

  return {
    kind: "ok",
    value: {
      name: rec.name as string,
      brand: rec.brand as string,
      scope,
      since: "",
      created: rec.created === true,
      token: rec.token as string,
    },
  };
}

function readIdentity(parsed: unknown, path: string): Answer<Identity> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");

  for (const field of ["name", "brand"] as const) {
    if (!isFilled(rec[field])) return notExpected(path, `нет строки «${field}»`);
  }
  const scope = readScope(rec.scope);
  if (!scope) return notExpected(path, "нет объекта «scope» с полями addr, here, path");

  return {
    kind: "ok",
    value: {
      name: rec.name as string,
      brand: rec.brand as string,
      scope,
      // `since` необязательно: без даты вход показать можно, соврать датой — нельзя.
      since: typeof rec.since === "string" ? rec.since : "",
    },
  };
}

function readScope(raw: unknown): ScopeView | null {
  const rec = asRecord(raw);
  if (!rec) return null;
  if (typeof rec.addr !== "string" || typeof rec.path !== "string") return null;
  return { addr: rec.addr, here: rec.here === true, path: rec.path };
}

function readResources(parsed: unknown, path: string): Answer<Resource[]> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");
  return readResourceList(rec.resources, path);
}

function readAddedResource(
  parsed: unknown,
  path: string,
): Answer<{ added: Resource; resources: Resource[] }> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");

  const added = readOneResource(rec.resource);
  if (!added) return notExpected(path, "нет объекта «resource» с полями name, addr, door");

  const list = readResourceList(rec.resources, path);
  if (list.kind === "refusal") return list;

  return { kind: "ok", value: { added, resources: list.value } };
}

function readResourceList(raw: unknown, path: string): Answer<Resource[]> {
  if (!Array.isArray(raw)) return notExpected(path, "нет массива «resources»");
  const out: Resource[] = [];
  for (let i = 0; i < raw.length; i += 1) {
    const one = readOneResource(raw[i]);
    if (!one) return notExpected(path, `элемент ${i} массива «resources» не похож на ресурс`);
    out.push(one);
  }
  return { kind: "ok", value: out };
}

function readOneResource(raw: unknown): Resource | null {
  const rec = asRecord(raw);
  if (!rec) return null;
  if (!isFilled(rec.name)) return null;
  // `addr` у ресурса контроллера пуст ЗАКОННО, а `door` — это измеренное состояние двери, и
  // без него строка списка врала бы молчанием. Поэтому проверки у них разные.
  if (typeof rec.addr !== "string" || !isFilled(rec.door)) return null;
  return {
    name: rec.name as string,
    addr: rec.addr,
    here: rec.here === true,
    alive: rec.alive === true,
    door: rec.door as string,
  };
}

function readFields(parsed: unknown, path: string): Answer<Field[]> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");
  return readFieldList(rec.fields, path);
}

function readAddedField(
  parsed: unknown,
  path: string,
): Answer<{ added: Field; fields: Field[]; note: string }> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");

  const added = readOneField(rec.field);
  if (!added) return notExpected(path, "нет объекта «field» с именем");

  const list = readFieldList(rec.fields, path);
  if (list.kind === "refusal") return list;

  return {
    kind: "ok",
    // `note` контроллера говорит вслух, чего НЕ произошло: поле записано, но не поднято.
    // Проглотить эту строку значило бы оставить человека ждать поднятого поля.
    value: { added, fields: list.value, note: typeof rec.note === "string" ? rec.note : "" },
  };
}

function readFieldList(raw: unknown, path: string): Answer<Field[]> {
  if (!Array.isArray(raw)) return notExpected(path, "нет массива «fields»");
  const out: Field[] = [];
  for (let i = 0; i < raw.length; i += 1) {
    const one = readOneField(raw[i]);
    if (!one) return notExpected(path, `элемент ${i} массива «fields» не похож на поле`);
    out.push(one);
  }
  return { kind: "ok", value: out };
}

function readOneField(raw: unknown): Field | null {
  const rec = asRecord(raw);
  if (!rec) return null;
  if (!isFilled(rec.name)) return null;
  return { name: rec.name as string, created: typeof rec.created === "string" ? rec.created : "" };
}

/** Отказ, названный контроллером: `{"code":…,"why":…,"ways":[…]}`. Не та форма — не наш случай. */
function readRefusal(body: string): Refusal | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return null;
  }
  const rec = asRecord(parsed);
  if (!rec) return null;
  if (!isFilled(rec.code) || !isFilled(rec.why)) return null;

  // Выходы у чужого отказа берём его же: своих сюда не дописываем. Пусты — покажем без
  // блока «что делать», а не с пустым заголовком; и это дефект контроллера, не пульта.
  const ways = Array.isArray(rec.ways)
    ? rec.ways.filter((w): w is string => typeof w === "string" && w.trim() !== "")
    : [];

  const refusal: Refusal = {
    code: rec.code as string,
    why: rec.why as string,
    ways,
    said: "control",
  };
  if (isFilled(rec.from)) {
    refusal.from = rec.from as string;
  }
  return refusal;
}

// ── мелочи ───────────────────────────────────────────────────────────────────

/** Отказ, сочинённый самим пультом. Хотя бы один выход обязателен — это видно по сигнатуре. */
function refuse<T>(code: string, why: string, way: string, ...more: string[]): Answer<T> {
  return { kind: "refusal", refusal: { code, why, ways: [way, ...more], said: "panel" } };
}

function notExpected<T>(path: string, why: string): Answer<T> {
  return refuse(
    "answer-not-expected",
    `ответ по ${path} не той формы, которую держит контракт: ${why}`,
    "форма ответов — в control/README.md (таблица ручек из WORLD2-102)",
    "разошлась форма — это вопрос к зоне control: пульт её не додумывает и не подстраивается молча",
  );
}

function asRecord(raw: unknown): Record<string, unknown> | null {
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) return null;
  return raw as Record<string, unknown>;
}

function isFilled(raw: unknown): boolean {
  return typeof raw === "string" && raw !== "";
}

function describe(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause);
}

/** Чужое тело в отказ кладём куском: страница отказа не место для мегабайта HTML. */
function cut(body: string): string {
  const flat = body.replace(/\s+/g, " ").trim();
  return flat.length > 200 ? `${flat.slice(0, 200)}…` : flat;
}
