#!/usr/bin/env node

// harness-doctor.mjs — самопроверка установки харнесса. НЕ хук: запускается руками из корня репо.
//   node .claude/hooks/harness-doctor.mjs                 # общий отчёт
//   OMNIFIELD_SCOPE=<scope> node .claude/hooks/harness-doctor.mjs   # + кто ты при этом scope
//
// Печатает реальность (продукт · зоны+существование папок · твоя роль · регистрация хуков ·
// marker), чтобы не приходилось «понимать по описанию»: запустил — увидел. Zero-deps
// (node:* + harness-config).
//
// ЗАЧЕМ здесь проверка регистрации хуков. `.claude/settings.json` — общий файл клиента
// (permissions, env, свои хуки), и обвес кладёт его классом `placed-once`: один раз и дальше
// не трогает. Цена класса — новые хуки следующих версий обвеса приезжают в `.claude/hooks/`,
// а РЕГИСТРАЦИЯ их не приезжает, потому что живёт в том самом файле. Форма baser выразить это
// не может (режимов материализации в записи нет, `merge` отменён), поэтому деградацию делаем
// ГРОМКОЙ: доктор сверяет эталонный блок регистрации с настоящим settings.json и печатает
// строку, которую нужно дописать. Это не обход формы — механизм не подменяется, называется то,
// что механизм назвать не может (tasker:BRAIN2-41 §4).

import { existsSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { argv } from "node:process";
import { fileURLToPath } from "node:url";
import {
  checkpointsTarget,
  gitAccess,
  grabliTarget,
  knownScopes,
  loadConfig,
  needsOnboarding,
  overlappingZones,
  PLACEHOLDER_PRODUCT,
  parseYaml,
  pilotWorkspace,
  rejectedZoneNames,
  resolveScope,
  roleOf,
  serviceBase,
  validateConfig,
  zonePaths,
  zoneReality,
} from "./harness-config.mjs";

/** Личность обвеса (`package.json.baser.source.id`). Дубль объявления — но ГРОМКИЙ:
 *  расхождение с манифестом ловит contract.test.mjs. Личность стабильна по построению,
 *  имя пакета — нет (форма §1: имя это доставка, id это личность). */
export const SOURCE_ID = "brainer/harness";
/** Имя эталонного блока регистрации внутри contentRoot обвеса. */
export const REGISTRATION_BLOCK = "settings.hooks.json";
const CONSUMER_SETTINGS = join(".claude", "settings.json");

// --- регистрация хуков: эталон, факт, расхождение ----------------------------

/** Плоский список объявленных регистраций: {event, matcher, command}. */
export function declaredRegistrations(block) {
  const out = [];
  for (const [event, groups] of Object.entries(block?.hooks ?? {})) {
    if (!Array.isArray(groups)) continue;
    for (const group of groups) {
      const matcher = group?.matcher ?? null;
      for (const hook of group?.hooks ?? []) {
        if (typeof hook?.command === "string") out.push({ event, matcher, command: hook.command });
      }
    }
  }
  return out;
}

/** Зарегистрирована ли ровно эта тройка (событие · matcher · команда) в settings.json. */
export function isRegistered(settings, { event, matcher, command }) {
  const groups = settings?.hooks?.[event];
  if (!Array.isArray(groups)) return false;
  return groups.some(
    (g) => (g?.matcher ?? null) === matcher && (g?.hooks ?? []).some((h) => h?.command === command),
  );
}

/** Что объявлено эталоном, но не зарегистрировано у потребителя. */
export function missingRegistrations(settings, block) {
  return declaredRegistrations(block).filter((r) => !isRegistered(settings, r));
}

/** Готовая строка, которую человеку нужно дописать в `.claude/settings.json`. */
export function registrationFix(missing) {
  const byEvent = new Map();
  for (const r of missing) {
    if (!byEvent.has(r.event)) byEvent.set(r.event, []);
    byEvent.get(r.event).push(r);
  }
  const lines = [];
  for (const [event, regs] of byEvent) {
    const groups = [];
    const byMatcher = new Map();
    for (const r of regs) {
      if (!byMatcher.has(r.matcher)) byMatcher.set(r.matcher, []);
      byMatcher.get(r.matcher).push(r.command);
    }
    for (const [matcher, commands] of byMatcher) {
      const group = matcher === null ? {} : { matcher };
      group.hooks = commands.map((command) => ({ type: "command", command }));
      groups.push(group);
    }
    lines.push(`"${event}": ${JSON.stringify(groups)}`);
  }
  return lines;
}

/**
 * Где лежит эталонный блок регистрации. Объявление у него ОДНО (`settings.hooks.json` в
 * contentRoot обвеса) — ищем его там, где в этой раскладке живёт содержимое обвеса:
 *   1) рядом с самим доктором (`harness/hooks/` → `harness/`) — исходник и ручной бандл;
 *   2) у потребителя доктор лежит в `.claude/hooks/`, а содержимое — в пакете: находим пакет
 *      по ЛИЧНОСТИ среди объявленных в `baser.json` (имя пакета — доставка, оно переезжает).
 * Не нашли — говорим вслух: проверка НЕ выполнена (молчаливый зелёный хуже отсутствующего).
 */
export function findRegistrationBlock(cwd, moduleUrl) {
  const sibling = fileURLToPath(new URL(`../${REGISTRATION_BLOCK}`, moduleUrl));
  if (existsSync(sibling)) return { path: sibling, via: "рядом с доктором (contentRoot обвеса)" };

  const consumerManifest = join(cwd, "package.json");
  if (!existsSync(consumerManifest)) return null;
  let sources = [];
  try {
    sources = JSON.parse(readFileSync(join(cwd, "baser.json"), "utf8"))?.sources ?? [];
  } catch {
    return null;
  }
  const require = createRequire(consumerManifest);
  for (const entry of sources) {
    const use = entry?.use;
    if (typeof use !== "string") continue;
    try {
      const manifestPath = require.resolve(`${use}/package.json`);
      const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
      if (manifest?.baser?.source?.id !== SOURCE_ID) continue;
      const path = join(
        dirname(manifestPath),
        manifest.baser.source.contentRoot,
        REGISTRATION_BLOCK,
      );
      if (existsSync(path)) return { path, via: `пакет ${use}` };
    } catch {
      // пакет не резолвится / манифест не читается — идём к следующему источнику
    }
  }
  return null;
}

/** Отчёт по регистрации хуков: строки для печати. */
export function registrationReport(cwd, moduleUrl, { ok, bad, warn }) {
  const lines = [];
  const ref = findRegistrationBlock(cwd, moduleUrl);
  if (!ref) {
    lines.push(warn("эталонный блок регистрации не найден — проверка хуков НЕ выполнена"));
    lines.push(
      `    → он живёт в пакете обвеса (${REGISTRATION_BLOCK}); поставь пакет либо запусти доктора из бандла.`,
    );
    return lines;
  }
  let block;
  try {
    block = JSON.parse(readFileSync(ref.path, "utf8"));
  } catch {
    lines.push(bad(`эталонный блок регистрации не читается: ${ref.path}`));
    return lines;
  }

  const settingsPath = join(cwd, CONSUMER_SETTINGS);
  let settings = null;
  try {
    settings = JSON.parse(readFileSync(settingsPath, "utf8"));
  } catch {
    settings = null;
  }
  if (settings === null) {
    lines.push(bad(`${CONSUMER_SETTINGS} не найден/не читается — ни один хук не подключён`));
    lines.push("    → положи файл заново (обвес кладёт его один раз, класс placed-once).");
    return lines;
  }

  const declared = declaredRegistrations(block);
  const missing = missingRegistrations(settings, block);
  if (!missing.length) {
    lines.push(
      ok(
        `хуки зарегистрированы в ${CONSUMER_SETTINGS}: ${declared.length}/${declared.length} (эталон — ${ref.via})`,
      ),
    );
  } else {
    lines.push(bad(`хуки НЕ зарегистрированы: ${missing.length} из ${declared.length}`));
    for (const r of missing)
      lines.push(`    - ${r.event}${r.matcher ? ` [${r.matcher}]` : ""} → ${r.command}`);
    lines.push(`    ПРИЧИНА: ${CONSUMER_SETTINGS} кладётся один раз (placed-once) — регистрация`);
    lines.push("    новых хуков сама не приезжает. Допиши в `hooks` вручную:");
    for (const line of registrationFix(missing)) lines.push(`      ${line}`);
  }

  // Обратная сторона: зарегистрированная команда указывает на несуществующий файл.
  const stale = declaredRegistrations(settings)
    .filter((r) => /\.claude[/\\]hooks[/\\]/.test(r.command))
    .filter((r) => {
      const file = /(\.claude[/\\]hooks[/\\][\w.-]+)/.exec(r.command)?.[1];
      return file && !existsSync(resolve(cwd, file));
    });
  for (const r of stale) lines.push(warn(`зарегистрирован отсутствующий хук: ${r.command}`));

  return lines;
}

// --- машинный pre-commit: есть он или его нет (BRAIN2-64) --------------------
//
// ГРАНИЦА, названная вслух, чтобы через месяц её не «дочинили»: git-хуки потребителя — ЕГО
// территория. Обвес их НЕ везёт и молча не ставит. Рынок здесь единодушен (сверено 2026-08-01):
// husky ставится явным `npx husky init` + `prepare` в манифесте ПОТРЕБИТЕЛЯ, lefthook —
// `lefthook install`, python-фреймворк pre-commit — `pre-commit install`. Ни один не приезжает
// хуком за компанию с зависимостью: инструмент, который ты завёл, и хук, который тебе положили
// в `.git/`, — разные вещи, и второе никто не делает.
//
// Наше дело — сказать ПРАВДУ о том, есть машина или нет: рамка требует зелёный pre-commit, и
// агент, который считает, что его проверят, ведёт себя иначе, чем тот, кто знает, что не
// проверят (случай owner-сессии baser, tasker:BRAIN2-52).

/** Каталог `.git`: папка либо файл `gitdir: …` (worktree/submodule). null — репозитория нет. */
export function gitDirOf(cwd) {
  const dot = join(cwd, ".git");
  if (!existsSync(dot)) return null;
  try {
    const text = readFileSync(dot, "utf8"); // папку прочитать не выйдет → catch ниже
    const ref = /^gitdir:\s*(.+)$/m.exec(text)?.[1]?.trim();
    return ref ? resolve(cwd, ref) : null;
  } catch {
    return dot; // это папка
  }
}

/** `core.hooksPath` из `.git/config` (INI: секция `[core]`), либо null. */
export function hooksPathOf(gitDir) {
  let text;
  try {
    text = readFileSync(join(gitDir, "config"), "utf8");
  } catch {
    return null;
  }
  let section = null;
  for (const line of text.split(/\r?\n/)) {
    const head = /^\s*\[([^\]]+)\]/.exec(line);
    if (head) {
      section = head[1].trim().toLowerCase();
      continue;
    }
    if (section !== "core") continue;
    const kv = /^\s*hooksPath\s*=\s*(.+?)\s*$/i.exec(line);
    if (kv) return kv[1];
  }
  return null;
}

/**
 * Состояние машинного pre-commit у потребителя:
 *   { repo, live, via, dormant } — есть ли репозиторий · видит ли git хук · откуда · и не лежит
 *   ли хук ФАЙЛОМ мимо git'а (типовой случай: `.husky/pre-commit` есть, а `prepare` не выполнен,
 *   то есть машина выглядит установленной, но не работает — молчаливая деградация).
 */
export function precommitStatus(cwd) {
  const gitDir = gitDirOf(cwd);
  if (!gitDir) return { repo: false, live: false, via: null, dormant: null };
  const configured = hooksPathOf(gitDir);
  const candidates = [
    ...(configured
      ? [{ dir: resolve(cwd, configured), via: `core.hooksPath = ${configured}` }]
      : []),
    { dir: join(gitDir, "hooks"), via: ".git/hooks/pre-commit" },
  ];
  for (const c of candidates)
    if (existsSync(join(c.dir, "pre-commit")))
      return { repo: true, live: true, via: c.via, dormant: null };
  const husky = join(cwd, ".husky", "pre-commit");
  return {
    repo: true,
    live: false,
    via: null,
    dormant: existsSync(husky) ? ".husky/pre-commit" : null,
  };
}

/** Отчёт по машинному pre-commit: строки для печати. */
export function precommitReport(cwd, { ok, bad, warn }) {
  const s = precommitStatus(cwd);
  if (!s.repo) return [warn("git-репозиторий не найден — про машинный pre-commit сказать нечего")];
  if (s.live) return [ok(`машинный pre-commit подключён (${s.via})`)];
  const lines = s.dormant
    ? [
        bad(`машинный pre-commit НЕ работает: файл ${s.dormant} есть, но git его не видит`),
        "    (`core.hooksPath` не настроен — установка инструмента не доведена до конца).",
      ]
    : [bad("машинного pre-commit НЕТ — коммит в этом репозитории не проверит никто")];
  lines.push(
    "    Рамка требует зелёный pre-commit (test+lint+build) — это ТРЕБОВАНИЕ К ПРОДУКТУ, а не",
    "    обещание обвеса: git-хуки твоя территория, и обвес их не везёт. Заведи машину сам",
    "    (`npx husky init` · `lefthook install` · `pre-commit install`) — до тех пор каденс",
    "    держится на агенте: test/lint/build руками перед каждым коммитом.",
  );
  return lines;
}

// --- отчёт -------------------------------------------------------------------

export function report(cwd, moduleUrl) {
  const out = [];
  const p = (s = "") => out.push(s);
  const ok = (s) => `  ✓ ${s}`;
  const bad = (s) => `  ✗ ${s}`;
  const warn = (s) => `  ⚠ ${s}`;

  p("harness doctor — проверка установки");
  p(`repo (cwd): ${cwd}`);
  p("");

  // --- конфиг ----------------------------------------------------------------
  const yamlPath = join(cwd, ".omnifield", "harness.yaml");
  let raw = null;
  try {
    raw = parseYaml(readFileSync(yamlPath, "utf8"));
  } catch {
    raw = null;
  }
  const config = loadConfig(cwd);

  if (!raw) {
    p(bad(".omnifield/harness.yaml не найден/не читается"));
    p("    → main-сессия заведётся на дефолте; owner-сессии НЕ смогут стартовать (нет зон).");
  } else {
    p(ok(".omnifield/harness.yaml прочитан"));
    const av = raw.apiVersion ?? "(нет)";
    const kind = raw.kind ?? "(нет)";
    p(`    apiVersion: ${av} · kind: ${kind}  (справочно — станком не валидируются)`);
  }
  p("");

  // --- продукт ---------------------------------------------------------------
  // Плейсхолдер шаблона — НЕ заполненный продукт. Раньше здесь стояла зелёная галочка на
  // `my-product`, пока баннер тот же конфиг уводил в онбординг: два инструмента говорили про
  // одно состояние разное, причём зелёный был у того, которым установку ПРОВЕРЯЮТ (BRAIN2-46 §1).
  if (needsOnboarding(config)) {
    p(
      warn(
        config.product
          ? `продукт: ${config.product} — это ПЛЕЙСХОЛДЕР шаблона (\`${PLACEHOLDER_PRODUCT}\`), сид не заполнен`
          : "продукт не задан (`product:` пуст) — сид не заполнен",
      ),
    );
    p("    → architect стартует в ОНБОРДИНГ-режим; owner'ов не поднимаем (зоны — placeholder).");
  } else {
    p(ok(`продукт: ${config.product}`));
  }
  p(`архитекторов сконфигурено: ${config.architects}`);
  const grabli = grabliTarget(config);
  if (grabli) p(ok(`grabli-ws: ${grabli} (затыки/грабли пишем сюда)`));
  else p(warn("grabli-ws не задан (`grabli.workspace`) — канал записи граблей не сконфигурен"));
  const tsk = serviceBase(config, "tasker");
  const kb = serviceBase(config, "knowledger");
  if (tsk || kb) {
    p(ok(`services (доступ curl'ом, НЕ MCP): tasker=${tsk ?? "—"} · knowledger=${kb ?? "—"}`));
    p(
      `    проверь связь: curl -s ${tsk ?? "<tasker>"}/healthz  (нет ответа → сэндбокс off / смени адрес)`,
    );
  } else {
    p(warn("services не заданы (`services.tasker/.knowledger`) — базы сервисов не сконфигурены"));
  }
  const pilotKb = pilotWorkspace(config, "knowledger");
  const pilotTsk = pilotWorkspace(config, "tasker");
  if (pilotKb || pilotTsk) {
    p(
      ok(
        `раздел пилотов: knowledger=${pilotKb ?? "—"} · tasker=${pilotTsk ?? "—"} (ходит architect)`,
      ),
    );
  } else {
    p("  · раздел пилотов не задан (`pilots.tasker/.knowledger`) — правило молчит (штатно)");
  }
  const checkpoints = checkpointsTarget(config);
  if (checkpoints) {
    p(
      ok(
        `чекпойнты ролей: корень ${checkpoints.root}${checkpoints.workspace ? ` (ws ${checkpoints.workspace})` : ""} — адрес назовёт баннер роли`,
      ),
    );
  } else {
    p("  · чекпойнты не заданы (`checkpoints.root`) — про чекпойнт харнесс молчит (штатно)");
  }
  p("");

  // --- зоны ------------------------------------------------------------------
  const rejected = rejectedZoneNames(raw?.zones);
  if (rejected.length) {
    p(bad(`зоны с зарезервированными именами ОТВЕРГНУТЫ: ${rejected.join(", ")}`));
    p("    → переименуй (main/layer — служебные слова роль-модели).");
  }
  const zones = Object.entries(config.zones);
  if (!zones.length) {
    p(warn("зон нет — owner-сессии стартовать не смогут (только architect/main)"));
  } else {
    p(`зоны (${zones.length}):`);
    for (const [name, z] of zones) {
      const paths = zonePaths(z);
      if (!paths.length) {
        p(bad(`${name} → нет путей (пустой paths[])`));
        continue;
      }
      p(`  ${name}:`);
      for (const path of paths) {
        const exists = existsSync(join(cwd, path));
        p(`    ${exists ? "✓" : "✗"} ${path}${exists ? "" : "   ПАПКИ НЕТ (boundary на пустоту)"}`);
      }
    }
  }
  // Конфиг объявляет зоны, которых здесь нет НИ ОДНОЙ → он не от этого репозитория.
  // Отметки «ПАПКИ НЕТ» были и раньше по каждой зоне — но их никто не складывал в вывод.
  const reality = zoneReality(config, cwd);
  if (reality.foreign) {
    p("");
    p(bad("КОНФИГ, ПОХОЖЕ, НЕ ОТ ЭТОГО РЕПОЗИТОРИЯ"));
    p(`    объявлено путей: ${reality.declared}, существует: 0 — ни одной зоны здесь нет.`);
    p(
      `    Так выглядит harness.yaml, скопированный из соседнего продукта${config.product ? ` (\`product: ${config.product}\`)` : ""}:`,
    );
    p("    имя продукта, роадмап и зоны в нём — чужие. Проверь с user, прежде чем работать.");
  } else if (reality.declared && reality.present < reality.declared) {
    p(
      warn(
        `папок нет у ${reality.declared - reality.present} из ${reality.declared} объявленных путей — boundary на пустоту`,
      ),
    );
  }
  // Валидатор роль-модели (relative / непустой) — kb:BRAIN2-1.
  const cfgErrors = validateConfig(config);
  if (cfgErrors.length) {
    p("");
    p(bad(`валидатор роль-модели: ${cfgErrors.length} ошибок`));
    for (const e of cfgErrors) p(`    - ${e}`);
  } else if (zones.length) {
    p(ok("валидатор роль-модели: пути относительные и непустые"));
  }
  // Пересечение зон — НЕ ошибка: governance конфиг не валидирует и правку пускает. Раньше
  // мы называли это ошибкой «одна папка — один владелец» и тем обещали защиту, которой нет.
  const overlaps = overlappingZones(config);
  if (overlaps.length) {
    p(warn(`пути зон пересекаются (${overlaps.length}) — машинной границы между ними НЕТ:`));
    for (const o of overlaps)
      p(`    - "${o.zones[0]}" ↔ "${o.zones[1]}": "${o.paths[0]}" ↔ "${o.paths[1]}"`);
    p("    Задумано так (несколько ролей над одними файлами) — это законная раскладка.");
    p("    Не задумано — разведи paths[], иначе владельца у этих файлов машинно нет.");
  }
  p("");

  // --- регистрация хуков (цена класса placed-once, см. шапку) ----------------
  for (const line of registrationReport(cwd, moduleUrl, { ok, bad, warn })) p(line);
  p("");

  // --- машинный pre-commit (BRAIN2-64: рамка его требует, обвес его не везёт) -
  for (const line of precommitReport(cwd, { ok, bad, warn })) p(line);
  p("");

  // --- текущий scope ---------------------------------------------------------
  const scope = process.env.OMNIFIELD_SCOPE;
  if (!scope) {
    p("OMNIFIELD_SCOPE не задан — запусти `OMNIFIELD_SCOPE=main|<zone> ...` чтобы увидеть роль.");
    p(`доступные scope: ${knownScopes(config).join(", ")}`);
  } else {
    const resolved = resolveScope(scope, config);
    if (scope === "main") {
      p(ok(`OMNIFIELD_SCOPE=main → architect (git: ${gitAccess("main", config)})`));
    } else if (resolved?.kind === "zone") {
      p(
        ok(
          `OMNIFIELD_SCOPE=${scope} → owner-${scope} (git: ${gitAccess(scope, config)}), папки: ${resolved.paths.map((x) => `${x}/`).join(", ")}`,
        ),
      );
    } else {
      p(bad(`OMNIFIELD_SCOPE=${scope} НЕ резолвится в зону (роль: ${roleOf(scope)})`));
      p(`    доступные: ${knownScopes(config).join(", ")}`);
    }
  }
  p("");

  // --- marker ----------------------------------------------------------------
  const marker = join(cwd, ".claude", ".main-session-id");
  try {
    const ids = readFileSync(marker, "utf8")
      .split(/\r?\n/)
      .map((l) => l.trim())
      .filter(Boolean);
    p(ok(`marker .claude/.main-session-id есть (${ids.length} активн. main-сессий)`));
  } catch {
    p(`  · marker .claude/.main-session-id нет (появится на SessionStart architect-сессии)`);
  }

  return out.join("\n");
}

// main() ТОЛЬКО как скрипт — при import (тесты) не запускается.
if (fileURLToPath(import.meta.url) === argv[1]) {
  process.stdout.write(`${report(process.cwd(), import.meta.url)}\n`);
}
