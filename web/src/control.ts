// Разговор с КОНТРОЛЛЕРОМ — единственное место, где пульт узнаёт что-либо о мире.
//
// Пульт переехал от двери к контроллеру (`WORLD2` 3.7, задача `WORLD2-102`): дверь — вход и
// выход, она ничего не показывает и ничем не распоряжается; лицо для человека живёт при
// контроллере. Поэтому здесь нет ни одной ручки двери, и `/api/locations` из этой зоны ушёл
// вместе с экраном, который его показывал.
//
// Инвариант файла остался прежним и от переездов не зависит:
//
//	ПУЛЬТ ПОКАЗЫВАЕТ ТО, ЧТО ОТДАЛ КОНТРОЛЛЕР, и своего состояния мира не держит.
//
// Ни кэша, ни досчитанных полей, ни «ресурс наверняка ещё жив». Есть разбор ответа и перевод
// неответа в ОТКАЗ, который называет причину и выходы (`WORLD2` 2.3).
//
// СКОУП ЛЕЖИТ ПО АДРЕСУ, А КОНТРОЛЛЕР ПУСТ (`WORLD2` 3.4, 1.9, задача `WORLD2-132`).
// Личность юзера раздаётся раздачей на его машине, а контроллер до неё дотягивается: он
// времянка, и своего состояния у него нет. Для пульта это сменило три вещи разом:
//
//	вход — это АДРЕС РАЗДАЧИ И ПАРОЛЬ, и больше ничего;
//	«завести здесь» НЕ СУЩЕСТВУЕТ (`3.7`) — заведение это отдельная ручка и отдельный адрес;
//	выход — ручка, а не забытая метка: он снимает времянки контроллера.
//
// Контракт — `control/README.md` (разделы «Вход», «Завести скоуп», «Выход», «Территории»),
// пакет `control/internal/api`:
//
//	POST   /api/scope              завести скоуп ТАМ, где юзер его назвал: две пары и личность
//	POST   /api/session            вход: адрес раздачи и пароль
//	DELETE /api/session            выход: времянки контроллера снимаются, скоуп не трогается
//	GET    /api/me                 кто я сейчас
//	GET    /api/resources          территории юзера: имя, адрес, отвечает ли, что на них стоит
//	POST   /api/resources          завести территорию — на ней встаёт вещь (не названа — дверь)
//	GET    /api/fields             поля юзера
//	POST   /api/fields             завести поле
//
// `DELETE /api/resources/{имя}` у контроллера есть, а здесь его нет намеренно: снятие ресурса
// — действие необратимое на чужой машине, и `WORLD2-102` его на экран не просит. Появится
// оно вместе с тем, кто решит, как человек это подтверждает, а не тихой кнопкой рядом со
// списком.
//
// `GET /api/recipes` (чем контроллер умеет поднимать) и поле `recipe` у добавления ресурса
// здесь по той же причине не появились: выбирать вещь `WORLD2-131` на экран не просил.
// Прежний запрос пульта работает как работал — рецепт не назван, и контроллер ставит дверь.

/** Ручки контроллера. Те же пути, что в `control/internal/api`. */
export const PATH = {
  scope: "/api/scope",
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

/**
 * Где лежит скоуп — так, как это увидел контроллер.
 *
 * Адрес скоупа это ОБЫЧНЫЙ HTTP-АДРЕС РАЗДАЧИ, и состояние лежит в её корне (`WORLD2` 3.4):
 * две раздачи — два скоупа, а не два пути в одной. Прежней пары «лежит ли он здесь» и «путь
 * на машине» больше нет: она и означала личность, лежащую томом контроллера (`WORLD2-124`).
 */
export type ScopeView = {
  /** адрес раздачи, названный человеком: `http://10.8.0.5:8070/` */
  addr: string;
  /** машина, на которой раздача стоит, — так, как её разобрал контроллер */
  host: string;
};

/** Личность юзера — ровно то, что назвал канон (`WORLD2` 3.4): имя и бренд. */
export type Identity = {
  name: string;
  brand: string;
  scope: ScopeView;
  /** с какого времени длится вход; у ответа входа его нет */
  since: string;
};

/**
 * Вход состоялся. `created` — скоуп завели прямо сейчас, а не нашли по адресу.
 *
 * `note` — ЦЕНА, названная контроллером: он изменил чужую машину. Приезжает только на пути с
 * паролем (`WORLD2-141`) и только после того, как запись состоялась; пустая строка значит,
 * что менять было нечего. Пульт её не сокращает и не пересказывает.
 */
export type Session = Identity & { created: boolean; token: string; note: string };

/**
 * Выход состоялся. Своего состояния он не трогает: скоуп лежит там, где лежал, и открывается
 * тем же адресом и паролем. Снимаются ВРЕМЯНКИ контроллера, и он говорит об этом словами —
 * `note` показывается человеку, а не проглатывается.
 */
export type Leaving = { note: string };

/**
 * Вещь, поднятая на ресурсе. Своего перечня вещей пульт не держит и держать не может: какие
 * они бывают, не знает и контроллер (`WORLD2` 3.7, `WORLD2-131`) — вещь называет рецепт.
 */
export type Thing = {
  /** имя вещи — имя проекта компоуза на той машине; читается там же, где его читает сосед */
  name: string;
  /**
   * что видно про вещь СЛОВАМИ контроллера: «здорова», «поднимается», «нездорова», «запущена
   * не вся», «запущена, здоровья не спросить». Пульт их не переводит и не сокращает: свой
   * словарь того же самого разъехался бы с первоисточником на первой же правке соседа.
   */
  state: string;
  /**
   * вещь ОТВЕЧАЕТ, а не «запущена»: запуск подтверждает докер, ответ — `HEALTHCHECK` изнутри.
   * У вещи без `HEALTHCHECK`-а ответа не спросить вовсе, и тогда здесь `false` — «не
   * подтверждено», а не «мертва»; сказано это словами в `state`, и пульт их и показывает.
   */
  alive: boolean;
};

/**
 * Территория юзера глазами пульта: МАШИНА, до которой дотянулись. Памяти и ядер здесь нет
 * намеренно (`WORLD2` 2.5), двери нет потому, что ресурс перестал быть «машиной с дверью»
 * (`WORLD2-131`), а РЕСУРСА «ЗДЕСЬ» нет потому, что список берётся из СКОУПА (`WORLD2` 3.4):
 * машина контроллера юзеру не принадлежит — он времянка, и своё хозяйство в его личность не
 * подмешивает (`WORLD2-132`).
 */
export type Resource = {
  /** имя участка — его называет юзер, и в скоупе оно не повторяется (`WORLD2` 2.5 п. 11) */
  name: string;
  /** адрес машины: `world@10.8.0.5`. Участка без адреса не бывает — его называет юзер */
  addr: string;
  /** отвечает ли САМ ресурс: «отвечает» либо «молчит». Вопрос про машину, а не про вещь на ней */
  reach: string;
  /**
   * что на ресурсе поднято. `null` — «не спросили» (ресурс молчит), пустой список — «спросили,
   * и там ничего нет». РАЗНЫЕ ответы, и пульт их не схлопывает: пустой список вместо «не
   * спросили» — это знание, которого у нас нет (`WORLD2` 4.2).
   */
  things: Thing[] | null;
};

/**
 * Поле юзера. Пока это ЗАПИСЬ о поле, а не поднятое поле — так говорит и сам контроллер.
 *
 * Состав сменился вместе со ступенью 2: поля лежат в СКОУПЕ, формой мира (`имя` · `адрес` ·
 * `состояние`), и ручка отдаёт эти три. Прежней даты заведения в контракте нет — пульт её не
 * держит: поле, которого нет у мира, на экране было бы нашей выдумкой.
 *
 * `addr` и `state` пустыми БЫВАЮТ, и это законно: поле пока только записано, нигде не
 * поднято, и адреса с состоянием у него ещё нет.
 */
export type Field = { name: string; addr: string; state: string };

/**
 * Чем входят: АДРЕС РАЗДАЧИ И ПАРОЛЬ, и больше ничего (`WORLD2` 3.7).
 *
 * Поля «завести здесь» тут нет и не появится: разница между «состояние есть» и «состояния
 * нет» только в исходе, а спрашивается одно и то же. Контроллер такое поле не проглатывает
 * молча, а отказывает (`bad-body`) — молча проглоченное выглядело бы сработавшим.
 */
export type EnterRequest = { addr: string; password: string };

/**
 * Чем заводят скоуп — ТАМ, где он будет лежать.
 *
 * ПАР ДВЕ, И ПУТАТЬ ИХ ДОРОГО (`WORLD2` 3.4, «Два адреса»): машина — это куда контроллер
 * дотянется и поднимет раздачу; скоуп — это адрес, по которому потом входить. Креды машины
 * юзер даёт руками ровно один раз, потому что скоупа в этот момент ещё нет; дальше они лежат
 * в нём. На слиянии этих двух пар выросла мёртвая `WORLD2-77`.
 *
 * `machine` НЕОБЯЗАТЕЛЬНА: раздачу юзер вправе поднять сам — это его вилка, и мир в неё не
 * смотрит (`WORLD2` 0.3). Называется она целиком или не называется вовсе, и решает это
 * человек явно, а не пульт по пустоте полей.
 */
export type CreateScopeRequest = {
  scope: { addr: string; password: string };
  identity: { name: string; brand: string };
  machine?: { name: string; addr: string; creds: Creds };
};

/**
 * Вид кред к машине. Называется ЯВНО и юзером (`WORLD2-141`, решение user): два вида, как в
 * PuTTY, — свой ключ либо пароль машины.
 *
 * Угадывать вид по виду строки нельзя, и не только контроллеру: угаданный однажды примет
 * ключ за пароль, и разбираться человек будет с отказом ssh, а не с нашей догадкой. Не
 * назван — контроллер отказывает `no-creds-kind`, назван неизвестный — `bad-creds-kind`.
 */
export type CredsKind = "key" | "password";

/**
 * Креды к машине: вид и значение.
 *
 * **У двух видов разная цена, и она не в наших словах, а в том, что произойдёт.** `key` не
 * меняет на машине юзера ничего. `password` — заход паролем ОДИН раз и **строка в чужом
 * `~/.ssh/authorized_keys`**; сам пароль дальше не участвует ни в чём и нигде не сохраняется.
 * Сказать об этом ДО кнопки — работа лица, потому что жмут её здесь (`WORLD2` 2.3, 0.1).
 */
export type Creds = { kind: CredsKind; value: string };

/**
 * Чем заводят территорию: ТРИ вещи, а не две (`WORLD2` 2.5 п. 11) — имя участка, адрес
 * машины и креды к ней. Имя не выводится из адреса и не подставляется молча: на нём стоит
 * адрес локации. Креды обязательны — без них контроллер отказывает (`no-creds`).
 */
export type AddResourceRequest = { name: string; addr: string; creds: Creds };

/**
 * Всё, что пульт умеет спросить у мира. Интерфейсом, а не набором функций, ради одного:
 * пробы подставляют его целиком и вызывают КАЖДЫЙ отказ нарочно (`WORLD2` 4.2 — проба,
 * которую ни разу не заставили упасть, пробой не является).
 */
export type Control = {
  enter(req: EnterRequest): Promise<Answer<Session>>;
  createScope(req: CreateScopeRequest): Promise<Answer<Session>>;
  leave(): Promise<Answer<Leaving>>;
  me(): Promise<Answer<Identity>>;
  resources(): Promise<Answer<Resource[]>>;
  addResource(
    req: AddResourceRequest,
  ): Promise<Answer<{ added: Resource; resources: Resource[]; note: string }>>;
  fields(): Promise<Answer<Field[]>>;
  addField(name: string): Promise<Answer<{ added: Field; fields: Field[]; note: string }>>;
};

/** Как ходить в сеть. Параметром — по той же причине, что и `Control`. */
export type Fetcher = (input: string, init?: RequestInit) => Promise<Response>;

const browserFetch: Fetcher = (input, init) => globalThis.fetch(input, init);

/** Куда писать трассу. Параметром — чтобы проба читала её, а не консоль раннера. */
export type Logf = (line: string) => void;

const browserLog: Logf = (line) => console.debug(line);

/** Глагол запроса. Своего значения не несёт — это глагол контракта, и он назван явно. */
type Method = "GET" | "POST" | "DELETE";

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
    method: Method,
    path: string,
    read: (parsed: unknown, path: string) => Answer<T>,
    body?: unknown,
  ) => call(doFetch, method, path, token, read, body, logf);

  /** Вход и заведение отдают одно и то же: личность, скоуп и метку сессии. */
  const запомнить = (answer: Answer<Session>): Answer<Session> => {
    if (answer.kind === "ok") {
      token = answer.value.token;
    }
    return answer;
  };

  return {
    async enter(req) {
      return запомнить(await ask("POST", PATH.session, readSession, req));
    },
    async createScope(req) {
      return запомнить(await ask("POST", PATH.scope, readSession, req));
    },
    async leave() {
      const answer = await ask("DELETE", PATH.session, readLeaving);
      // Метку роняем ТОЛЬКО на состоявшемся выходе. Выход отказал — времянки контроллера не
      // сняты, сессия жива, и забытая здесь метка сделала бы её недостижимой: человек видел
      // бы экран входа, а следующий вошедший — чужие территории.
      if (answer.kind === "ok") {
        token = "";
      }
      return answer;
    },
    me: () => ask("GET", PATH.me, readIdentity),
    resources: () => ask("GET", PATH.resources, readResources),
    // Список приезжает ТЕМ ЖЕ ответом — второй раз не спрашиваем: главное, ради чего человек
    // жал кнопку, — увидеть, что источников стало два (`WORLD2-80`), и лишний запрос между
    // действием и этим видом добавляет только ещё одну возможность не ответить.
    addResource: (req) => ask("POST", PATH.resources, readAddedResource, req),
    fields: () => ask("GET", PATH.fields, readFields),
    addField: (name) => ask("POST", PATH.fields, readAddedField, { name }),
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
  method: Method,
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
      `пульт: ${method} ${path} http=${http} ` +
        `refusal=${код} dur=${(сейчас() - начало).toFixed(1)}ms`,
    );
    return answer;
  };

  const init: RequestInit = { method };
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
//
// ПОЛЕ ЕСТЬ И ПУСТО — ЭТО НЕ «ПОЛЯ НЕТ» (`WORLD2` 3.4, задача `WORLD2-135`). Пустое законно
// везде в мире, и мерка у каждого поля своя:
//
//	`typeof … === "string"` — пустая строка ЗАКОННОЕ ЗНАЧЕНИЕ (бренд);
//	`isFilled`              — пустой строки у поля не бывает, и пустая значит поломку.
//
// Мерка выбирается не на вкус: она берётся оттуда, где поле рождается. Требование непустоты,
// поставленное на поле, которому пустым быть можно, — это отказ на ПРАВИЛЬНОМ ответе мира;
// именно им пульт не пускал в мир всякого, кто входит впервые.

function readSession(parsed: unknown, path: string): Answer<Session> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");

  // `name` и `token` пустыми не бывают: личность без имени контроллер не заводит вовсе
  // (отказ `no-name`), а пустая метка сессии означала бы «вошёл», когда входа нет.
  for (const field of ["name", "token"] as const) {
    if (!isFilled(rec[field])) return notExpected(path, `нет строки «${field}»`);
  }
  // `brand` — ЛЮБАЯ строка, в том числе пустая. Бренд это то, под чем юзер отдаёт своё; пока
  // он ничего не отдавал, бренда у него нет, и свежесозданный скоуп так и отвечает: `""`.
  // Это законное состояние мира, а не отсутствие данных, и выдумывать за юзера бренд (имя,
  // прочерк) пульт не станет — пустое показывается пустым и называется словами.
  if (typeof rec.brand !== "string") return notExpected(path, "нет строки «brand»");

  const scope = readScope(rec.scope);
  if (!scope) return notExpected(path, "нет объекта «scope» с полями addr, host");

  return {
    kind: "ok",
    value: {
      name: rec.name as string,
      brand: rec.brand as string,
      scope,
      since: "",
      created: rec.created === true,
      token: rec.token as string,
      // `note` есть только тогда, когда контроллер и правда изменил чужую машину: заведение
      // по ключу не меняет на ней ничего, и цены у него нет. Отсутствие — не поломка формы.
      note: typeof rec.note === "string" ? rec.note : "",
    },
  };
}

function readIdentity(parsed: unknown, path: string): Answer<Identity> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");

  // Мерка та же, что и у входа: имя непустое, бренд — любая строка (см. `readSession`).
  if (!isFilled(rec.name)) return notExpected(path, "нет строки «name»");
  if (typeof rec.brand !== "string") return notExpected(path, "нет строки «brand»");

  const scope = readScope(rec.scope);
  if (!scope) return notExpected(path, "нет объекта «scope» с полями addr, host");

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

/**
 * Адрес скоупа и машина, на которой он раздаётся, — обе строки НЕПУСТЫЕ, и мерка эта взята
 * оттуда, где поля рождаются: адрес называет человек, и без него контроллер отказывает
 * (`no-address`), а машину он вынимает из этого же адреса и отказывает, если её там нет
 * (`bad-address`). Пустым здесь ни то ни другое не приезжает.
 */
function readScope(raw: unknown): ScopeView | null {
  const rec = asRecord(raw);
  if (!rec) return null;
  if (!isFilled(rec.addr) || !isFilled(rec.host)) return null;
  return { addr: rec.addr as string, host: rec.host as string };
}

/**
 * Выход состоялся. `out` спрашиваем БУЛЕВЫМ и строго: им контроллер говорит, что времянки
 * сняты, — а не сняты они значит, что следующий вошедший увидит чужие территории. Догадка в
 * пользу «наверное, вышли» стоила бы ровно того, ради чего ступень 2 и делалась.
 */
function readLeaving(parsed: unknown, path: string): Answer<Leaving> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");
  if (rec.out !== true) return notExpected(path, "нет «out»: контроллер не сказал, что вышли");
  // `note` говорит вслух, чего выход НЕ тронул: скоуп лежит там, где лежал. Проглотить эту
  // строку значило бы оставить человека гадать, не стёрлась ли вместе с сессией личность.
  return { kind: "ok", value: { note: typeof rec.note === "string" ? rec.note : "" } };
}

function readResources(parsed: unknown, path: string): Answer<Resource[]> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");
  return readResourceList(rec.resources, path);
}

function readAddedResource(
  parsed: unknown,
  path: string,
): Answer<{ added: Resource; resources: Resource[]; note: string }> {
  const rec = asRecord(parsed);
  if (!rec) return notExpected(path, "это не объект");

  const added = readOneResource(rec.resource);
  if (!added) return notExpected(path, "нет объекта «resource» с полями name, addr, reach, things");

  const list = readResourceList(rec.resources, path);
  if (list.kind === "refusal") return list;

  // `note` — что изменено на ЧУЖОЙ машине (`WORLD2-141`). Приезжает только на пути с паролем;
  // проглотить эту строку значило бы смолчать о том, что мир тронул машину юзера.
  return {
    kind: "ok",
    value: { added, resources: list.value, note: typeof rec.note === "string" ? rec.note : "" },
  };
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
  // Имя участка называет ЮЗЕР, и пустым оно не бывает: сосед отсекает его правилом имени
  // (`bad-name`), а занятое — отказом механики (`name-taken`).
  if (!isFilled(rec.name)) return null;
  // Адрес участка тоже НЕПУСТОЙ — и это сменилось вместе со ступенью 2 (`WORLD2-132`): пустым
  // он бывал у ресурса «здесь», машины контроллера, а её в списке больше нет. Список — это
  // территории юзера из его скоупа, и каждую он завёл по адресу, который назвал сам.
  // `reach` — измеренный ответ самого ресурса, и без него строка списка врала бы молчанием.
  if (!isFilled(rec.addr) || !isFilled(rec.reach)) return null;

  const things = readThings(rec.things);
  if (things === НЕ_ТА_ФОРМА) return null;

  return {
    name: rec.name as string,
    addr: rec.addr as string,
    reach: rec.reach as string,
    things,
  };
}

/**
 * Отдельное значение для «форма разъехалась»: у списка вещей три исхода, а не два, и `null`
 * из них — законный ответ контроллера, а не поломка. Без третьего значения «не спросили» и
 * «поля нет» слились бы в одно, и пульт начал бы отвечать за контроллер.
 */
const НЕ_ТА_ФОРМА = Symbol("не та форма");

/**
 * Что стоит на ресурсе: `null` — «не спросили», список — «спросили, вот что нашли».
 *
 * Отсутствующее поле — это НЕ «не спросили»: контроллер говорит `null` вслух, и подменять
 * молчание контракта его словами значит сочинять за него ответ. Такой ответ — отказ.
 */
function readThings(raw: unknown): Thing[] | null | typeof НЕ_ТА_ФОРМА {
  if (raw === null) return null;
  if (!Array.isArray(raw)) return НЕ_ТА_ФОРМА;

  const out: Thing[] = [];
  for (const item of raw) {
    const one = readOneThing(item);
    if (!one) return НЕ_ТА_ФОРМА;
    out.push(one);
  }
  return out;
}

function readOneThing(raw: unknown): Thing | null {
  const rec = asRecord(raw);
  if (!rec) return null;
  // Имя вещи — имя проекта компоуза, и пустым оно не приезжает: контейнеры без этой метки
  // контроллер в список не берёт вовсе. `state` — измеренное состояние вещи словами; строка о
  // вещи без него говорила бы «вещь есть» и молчала о том, что про неё ничего не известно.
  if (!isFilled(rec.name) || !isFilled(rec.state)) return null;
  // `alive` спрашиваем БУЛЕВЫМ, а не «похоже на правду»: `false` у контроллера значит в том
  // числе «HEALTHCHECK-а нет, ответа не спросить», и мягкая проверка (`!== false`) выдала бы
  // неспрошенное за здоровье. Разъехалась форма — это отказ, а не догадка в пользу здоровья.
  if (typeof rec.alive !== "boolean") return null;
  return { name: rec.name as string, state: rec.state as string, alive: rec.alive };
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
  // Поле заводится ПО ИМЕНИ, безымянного контроллер не заводит (отказ `no-name`).
  if (!isFilled(rec.name)) return null;
  // Адреса и состояния у ЗАВЕДЁННОГО поля нет вовсе: ответ на заведение приезжает одним
  // именем, а в списке те же поля лежат пустыми, пока поле никуда не поднято. Требовать их
  // здесь значило бы отказать на правильном ответе мира — той же ошибкой, что стоила входа
  // всякому новому юзеру (`WORLD2-135`).
  return {
    name: rec.name as string,
    addr: typeof rec.addr === "string" ? rec.addr : "",
    state: typeof rec.state === "string" ? rec.state : "",
  };
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
    "форма ответов — в control/README.md (разделы «Вход», «Завести скоуп», «Территории»)",
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
