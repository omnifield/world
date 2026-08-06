#!/usr/bin/env node
// scope-identity.mjs — SessionStart hook: инжектит identity-баннер по OMNIFIELD_SCOPE.
// Роль-модель (зоны/пины моделей/число архитекторов) — ДАННЫЕ из `.omnifield/harness.yaml`
// (kb:BRAIN2-12), НЕ хардкод. Роли-рамка (инварианты) — .claude/agents/{architect,owner,layer}.md.
//   - 'main' → architect;  <zone> → owner-<zone>;  невалид → anomaly;  ПУСТО → баннер «роли нет»
//     (не тишина: молчание на старте неотличимо от нормальной работы, BRAIN2-46 §3).
//
// Contract (SessionStart): stdout { hookSpecificOutput: { hookEventName, additionalContext } }.
// Subagents (Agent tool) SessionStart НЕ триггерят — их identity из subagent_type prompt'а.

import { argv } from "node:process";
import { fileURLToPath } from "node:url";
import {
  checkpointsTarget,
  knownScopes,
  loadConfig,
  nearestScopes,
  needsOnboarding,
  overlappingZones,
  pilotWorkspace,
  resolveScope,
  serviceBase,
  zoneReality,
} from "./harness-config.mjs";

// Признак незаполненного сида живёт в harness-config — одно знание на баннер и на доктора
// (BRAIN2-46 §1). Ре-экспорт: исторические импорты из этого модуля продолжают работать.
export { needsOnboarding };

function silent() {
  process.stdout.write("{}");
  process.exit(0);
}

function emit(additionalContext) {
  process.stdout.write(
    JSON.stringify({ hookSpecificOutput: { hookEventName: "SessionStart", additionalContext } }),
  );
  process.exit(0);
}

function modelLine(config, role) {
  const pin = config.models?.[role];
  return pin ? ` (модель-пин: \`${pin}\`)` : "";
}

function productLabel(config) {
  return config.product
    ? `продукта \`${config.product}\``
    : "этого продукта (имя не задано в `.omnifield/harness.yaml` → впиши `product:`)";
}

/**
 * Конфиг объявляет зоны, которых в этом репозитории нет НИ ОДНОЙ → он не отсюда (BRAIN2-46 §2).
 * Возвращает блок строк для баннера, либо `[]`. Идёт ПЕРВЫМ, до представления роли
 * (tasker:BRAIN2-51 §1): пока сомнение стоит после утверждения, читающий получает сначала
 * ложную личность, а потом оговорку к ней — и запоминает первую.
 */
export function foreignConfigWarning(config, cwd) {
  const reality = zoneReality(config, cwd);
  if (!reality.foreign) return [];
  const zones = [...new Set(reality.rows.map((r) => r.zone))].join(", ");
  return [
    `# ⚠️ КОНФИГ, ПОХОЖЕ, НЕ ОТ ЭТОГО РЕПОЗИТОРИЯ`,
    ``,
    `\`.omnifield/harness.yaml\` объявляет зоны (${zones}), и **ни одна их папка здесь не существует**`,
    `— объявлено путей: ${reality.declared}, найдено: 0. Так выглядит конфиг, скопированный из соседнего`,
    `продукта: имя продукта, роадмап и зоны в нём — чужие, а не «зоны ещё не созданы».`,
    ``,
    `**Action: STOP, спроси user.** Не иди в роадмап названного продукта и не заводи здесь`,
    `его зоны, пока не подтверждено, что конфиг верный. Разбор — \`node .claude/hooks/harness-doctor.mjs\`.`,
  ];
}

/**
 * Соседи по папке: зоны, чьи пути пересекаются с зоной этого scope. Владелец обязан узнать об
 * этом ИЗ БАННЕРА (tasker:BRAIN2-51 §2) — диагностика говорит честно, но её никто не запускает,
 * а внутри пересечения машинной границы нет и от взаимного затирания ничто не защищает.
 * Возвращает блок строк, либо `[]`.
 */
export function overlapNotice(config, scope) {
  const neighbours = overlappingZones(config)
    .filter((pair) => pair.zones.includes(scope))
    .map((pair) => {
      const self = pair.zones[0] === scope ? 0 : 1;
      return { zone: pair.zones[1 - self], mine: pair.paths[self], theirs: pair.paths[1 - self] };
    });
  if (!neighbours.length) return [];
  return [
    ``,
    `## ⚠️ Ты делишь папку с другой зоной — границы между вами НЕТ`,
    ...neighbours.map((n) => `- с **owner-${n.zone}**: твой \`${n.mine}\` ↔ его \`${n.theirs}\``),
    ``,
    `Внутри пересечения governance пустит вас обоих: он держит границу зоны против ЧУЖИХ`,
    `папок, а не вас друг от друга. Значит от взаимного затирания не защищает ничто —`,
    `ни гейт, ни git (у вас общее дерево). Правишь общие файлы — сначала спроси user,`,
    `не идёт ли там работа. Пересечение не задумано — это вопрос к architect: сам`,
    `\`.omnifield/harness.yaml\` не правь, он вне твоей зоны.`,
  ];
}

/**
 * Адрес чекпойнта роли (tasker:BRAIN2-58). Правило рамки без адреса не исполняется: агент о
 * разделе просто не узнает, а «в конце сессии» вспоминать будет уже некому. Слот
 * `checkpoints` не объявлен → блок ПУСТОЙ: молчим, адрес не выдумываем (как с `grabli`).
 * Ведут чекпойнт только роль-сессии (architect/owner) — помощники живут внутри родителя.
 */
export function checkpointNotice(config, roleLabel) {
  const target = checkpointsTarget(config);
  if (!target) return [];
  const tsk = serviceBase(config, "tasker");
  const ws = target.workspace ? ` (ws \`${target.workspace}\`)` : "";
  return [
    ``,
    `## Чекпойнт роли — состояние прогона, корень \`${target.root}\`${ws}`,
    `- Узел на РОЛЬ (**${roleLabel}**), не на сессию: свой ищи среди детей корня, нового не заводи.`,
    ...(tsk
      ? [`  \`curl -s -H "Authorization: Bearer me" ${tsk}/nodes/${target.root}/children\``]
      : []),
    `- Пишешь **на закрытии этапа и перед длинной операцией**, не «в конце сессии»: сессия умирает посреди работы.`,
    `- Описание узла = ТЕКУЩЕЕ состояние (перезаписываешь), комменты = история. Рядом обязательно **ветка и SHA**.`,
    `- Форма: цель · статус · источник истины · что изменено (ветка/SHA) · что прогнано и чем подтверждено · чего не хватает · блокеры · следующее безопасное действие.`,
    `- Чекпойнт ≠ знания: здесь состояние прогона, решения и контракты — в knowledger.`,
    `- Подхватываешь чекпойнт — СВЕРЬ его с фактом (ветка, статусы узлов, реестр) ДО первого действия: он протухает молча.`,
  ];
}

/**
 * Готовый блок команд запуска — ОДИН формат на все случаи, когда сессию надо перезапустить
 * (роли нет · scope не резолвится). Второй формат тут был бы разным ответом на одно и то же
 * действие человека (tasker:BRAIN2-61). Имена выровнены по ширине — список читается столбиком.
 */
export function launchBlock(scopes) {
  const width = Math.max(...scopes.map((s) => s.length));
  const cmd = (s) => `OMNIFIELD_SCOPE=${s.padEnd(width)} claude`;
  return [
    `\`\`\`sh`,
    ...scopes.map((s) =>
      s === "main" ? `${cmd(s)}   # architect — владеет доставкой` : `${cmd(s)}   # owner-${s}`,
    ),
    `\`\`\``,
  ];
}

/**
 * Адрес раздела пилотов (tasker:BRAIN2-60) — ТОЛЬКО в architect-баннере: по рамке в раздел
 * ходит architect, owner — через него, и владельцу зоны эта строка была бы шумом. Правила
 * раздела здесь НЕ пересказываем (они в shared-policy, дубль разъедется) — только адрес и
 * отсылка к канону. Слот не объявлен — строки нет: адрес не выдумываем.
 */
export function pilotsNotice(config) {
  const kb = pilotWorkspace(config, "knowledger");
  const tsk = pilotWorkspace(config, "tasker");
  if (!kb && !tsk) return [];
  const where = [
    kb ? `знание — \`kb:${kb}\`` : null,
    tsk ? `работа и ступень — \`tasker:${tsk}\`` : null,
  ]
    .filter(Boolean)
    .join(", ");
  return [
    `- **Раздел пилотов** (в него ходишь ты, owner — через тебя): ${where}; правила — \`kb:PILOT-1\`.`,
  ];
}

function onboardingBanner(config) {
  return [
    `# Session identity — OMNIFIELD_SCOPE=main (architect · ОНБОРДИНГ)${modelLine(config, "architect")}`,
    ``,
    `Ты **architect/main**, но \`.omnifield/harness.yaml\` — ещё НЕЗАПОЛНЕННЫЙ общий шаблон`,
    `(\`product: ${config.product ?? "(пусто)"}\`, зоны/пины — placeholder). Это НЕ аномалия и НЕ повод паниковать:`,
    `**твоя первая задача — ОНБОРДИНГ, не работа.** Заполни роль-модель под этот продукт ВМЕСТЕ с user.`,
    ``,
    `## Порядок онбординга`,
    `1. **Пойми проект** (known-места, не спрашивая других агентов): \`README\`/\`*.md\`, \`package.json\`/манифесты,`,
    `   \`git log\`/структура папок. Определи реальные пакеты/папки — будущие зоны.`,
    `2. **Найди роадмап/канон продукта в сервисах** (если есть): tasker/knowledger ws по ИМЕНИ продукта`,
    `   (версия-приоритет — старшая, напр. \`FOO2\` > \`FOO\`). Нет — не выдумывай.`,
    `   Доступ — **curl'ом через Bash, НЕ MCP** (тулзов может не быть — норма); базы в \`services\``,
    `   (\`harness.yaml\`) или соседи \`:8030\`/\`:8040\`; нет связи → сэндбокс off / \`curl .../healthz\`.`,
    `3. **Предложи user конфиг** и заполните \`harness.yaml\` вместе: \`product\`, \`zones\` (\`paths[]\` из РЕАЛЬНЫХ`,
    `   папок, disjoint), \`models\` (по факту сессии), \`grabli.workspace\`. Решения по продукту — за **user**.`,
    `4. **Проект пустой** (нечего изучать) → поговори с user: что строим, какие зоны в планах → засидь под это.`,
    ``,
    `## Пока сид не заполнен`,
    `- **Owner'ов НЕ поднимаем** — governance режет по placeholder-путям, owner упрётся в стену.`,
    `- **Непонятно — спрашивай USER** (не другого агента; агенты друг друга не зовут).`,
    `- Проверка после заполнения: \`node .claude/hooks/harness-doctor.mjs\`.`,
    `- Обвязку (\`.claude/\`, \`.omnifield/harness.yaml\`) — закоммить в репу; артефакты установки`,
    `  (\`agent-harness-plugin/\`, демо-папки) — НЕ коммить (gitignore).`,
  ].join("\n");
}

function architectBanner(config) {
  return [
    `# Session identity — OMNIFIELD_SCOPE=main (architect)${modelLine(config, "architect")}`,
    ``,
    `Ты в роли **architect/main** ${productLabel(config)}. Правила роли — \`.claude/agents/architect.md\` + \`.claude/agents/shared-policy.md\`.`,
    `Роль-модель — данные \`.omnifield/harness.yaml\` (архитекторов сконфигурено: ${config.architects}).`,
    ``,
    `- Триаж запросов user; арх-решения и контракты пишешь в **knowledger**; координируешь овнеров **задачами в tasker** (\`tasker:KEY\`).`,
    `- **НЕ пиши код зон сам** — ставишь задачу овнеру в tasker → owner-сессию запускает user.`,
    `- Вся координация — через tasker, знания/решения — через knowledger. Локальных \`briefs/\`-файлов НЕ заводим (истина снаружи репо).`,
    `- Git: **владеешь доставкой** — ветка → PR → влитие, вливаешь только ты (права даёт marker \`.claude/.main-session-id\`).`,
    `  Это не обход правил: главная ветка защищена, прямой коммит в неё отказывают и тебе — заводи ветку и PR.`,
    `- Отдавая ТЗ овнеру, назови git-обвязку: **ветку заводишь ТЫ** (owner'у \`git checkout -b\` режет гейт), коммит ему можно, пуш/мерж — нет.`,
    ...pilotsNotice(config),
    ...checkpointNotice(config, "architect/main"),
  ].join("\n");
}

function ownerBanner(config, { scope, paths, name }) {
  const list = paths.length
    ? paths.map((p) => `\`${p}/\``).join(", ")
    : "`(зона без путей — аномалия конфига)`";
  const firstPath = paths[0] ?? "<зона>";
  return [
    `# Session identity — OMNIFIELD_SCOPE=${scope} (owner-${scope})${modelLine(config, "owner")}`,
    ``,
    `Ты в роли **owner-${scope}** ${productLabel(config)}, владелец зоны: ${list} (${name}).`,
    `**Ты НЕ architect** — правила роли architect не твои.`,
    ``,
    `## Зона (boundary)`,
    `- Edits — ТОЛЬКО внутри своих папок: ${list}. Любой файл вне них → STOP, верни state architect.`,
    `- Машинная граница (governance-хук) блокирует Edit/Write вне этих путей — не обходи, эскалируй ВВЕРХ.`,
    `- Перед первым Edit прочитай свой раздел в knowledger + \`${firstPath}/README.md\` (если есть).`,
    ...overlapNotice(config, scope),
    ``,
    `## Правила (канон)`,
    `- Первым читаешь \`.claude/agents/shared-policy.md\`.`,
    `- **НЕ пиши ADR**, не принимай cross-zone решения — это architect. Упёрлось → STOP + эскалация.`,
    `- **Git: commit-only** (под git-gate). Push/merge — architect после ревью. Conventional: \`feat(${scope}): ...\`.`,
    `- **Ветку заводит architect, не ты** (\`git checkout -b\` режет гейт): работай в той, что названа в ТЗ; не названа — спроси user.`,
    `- **Версию пакета не поднимаешь** — это architect при публикации; назови в комменте к задаче, какую считаешь правильной.`,
    `- Хук заблокировал git — НЕ обходи (\`bash -c\`/\`&&\`/\`--no-verify\`). STOP + эскалация.`,
    `- POLICY priority 0: никаких костылей, причина не следствие, DoD = код+тесты+трейсы+доки.`,
    ...checkpointNotice(config, `owner-${scope}`),
    ``,
    `## Скоуп задачи`,
    `Работаешь по задаче в **tasker** (\`tasker:KEY\`) или прямому ТЗ от user — НЕ по локальным файлам. Непонятен scope — STOP, спроси. Не угадывай.`,
  ].join("\n");
}

/**
 * Scope не резолвится. Чаще всего это ОПЕЧАТКА, а не отсутствующая зона, поэтому порядок
 * совета такой же, как у CLI рынка: сначала «перезапустись под верным именем» с готовой
 * командой, и только потом «а если зоны правда нет — впиши её в конфиг». Прежний порядок вёл
 * человека править конфиг из-за одного лишнего символа (tasker:BRAIN2-61).
 */
export function anomalyBanner(config, scope) {
  const scopes = knownScopes(config);
  const near = nearestScopes(scope, config);
  const lines = [
    `# Session identity — OMNIFIELD_SCOPE=${scope} (UNRESOLVED)`,
    ``,
    `**Аномалия**: scope "${scope}" не резолвится в зону (нет в \`.omnifield/harness.yaml\`).`,
    ``,
  ];
  if (near.length === 1) {
    lines.push(
      `Похоже на опечатку — **возможно, ты имел в виду \`${near[0]}\`**. Перезапусти сессию:`,
      ``,
      ...launchBlock(near),
      ``,
      `Не то — вот все доступные:`,
      ``,
    );
  } else if (near.length > 1) {
    lines.push(
      `Похоже на опечатку. Близкие по написанию: ${near.map((s) => `\`${s}\``).join(", ")} —`,
      `выбери нужный сам (за тебя не выбираем) и перезапусти сессию:`,
      ``,
      ...launchBlock(near),
      ``,
      `Не то — вот все доступные:`,
      ``,
    );
  } else {
    lines.push(
      `**Перезапусти сессию под верным scope** (похожего на "${scope}" среди них нет):`,
      ``,
    );
  }
  lines.push(
    ...launchBlock(scopes),
    ``,
    `Зоны действительно нет (это не опечатка) — тогда впиши её в \`.omnifield/harness.yaml\``,
    `(секция \`zones:\`) и перезапустись. Случай редкий: сперва проверь имя выше.`,
    ``,
    `**Action**: STOP. Сообщи user — scope невалидный. Не начинай работу (нет boundary/ownership).`,
  );
  return lines.join("\n");
}

/**
 * `OMNIFIELD_SCOPE` не задан. Раньше здесь была тишина: гейты закрывались правильно, но
 * человек узнавал об этом на первой правке, а пустой ответ хука неотличим от нормы
 * (BRAIN2-46 §3). Говорим сразу и с готовой командой запуска.
 */
export function noScopeBanner(config) {
  const scopes = knownScopes(config);
  return [
    `# Session identity — РОЛИ НЕТ (\`OMNIFIELD_SCOPE\` не задан)`,
    ``,
    `Сессия стартовала **без роли**: harness не знает, кто ты, и потому закрывает обе двери —`,
    `любая правка файла и любая git-запись получат \`deny\` с причиной «boundary неизвестна».`,
    `Это не поломка: гейты работают штатно, роли просто нет.`,
    ``,
    `**Action: STOP, не начинай работу.** Перезапусти сессию с ролью:`,
    ``,
    ...launchBlock(scopes),
    ``,
    scopes.length > 1
      ? `Доступные scope: ${scopes.join(", ")} (из \`.omnifield/harness.yaml\`).`
      : `Зон в \`.omnifield/harness.yaml\` нет — доступен только \`main\`. Разбор: \`node .claude/hooks/harness-doctor.mjs\`.`,
  ].join("\n");
}

/**
 * Сомнение вперёд, представление роли — под ним и с оговоркой. Порядок и есть содержание:
 * баннер, который сначала уверенно называет чужой продукт, а сомневается ниже, читается как
 * «ты architect продукта X, но есть нюанс» — то есть личность уже принята (tasker:BRAIN2-51 §1).
 */
function withForeignWarning(banner, config, cwd) {
  const warning = foreignConfigWarning(config, cwd);
  if (!warning.length) return banner;
  return [
    ...warning,
    ``,
    `---`,
    ``,
    `Ниже — представление роли **по этому же конфигу**: имя продукта, зоны и пины взяты`,
    `из него, и доверия ему сейчас нет. Читай как гипотезу, а не как свою личность.`,
    ``,
    banner,
  ].join("\n");
}

function main() {
  const cwd = process.cwd();
  const config = loadConfig(cwd);
  const scope = process.env.OMNIFIELD_SCOPE;
  if (!scope) return emit(withForeignWarning(noScopeBanner(config), config, cwd));
  if (scope === "main") {
    const banner = needsOnboarding(config) ? onboardingBanner(config) : architectBanner(config);
    return emit(withForeignWarning(banner, config, cwd));
  }
  const resolved = resolveScope(scope, config);
  if (resolved?.kind !== "zone") return emit(anomalyBanner(config, scope));
  return emit(withForeignWarning(ownerBanner(config, resolved), config, cwd));
}

// Исполняем main() ТОЛЬКО как скрипт (main вызывает process.exit) — при import безопасно.
if (fileURLToPath(import.meta.url) === argv[1]) {
  try {
    main();
  } catch {
    silent();
  }
}
