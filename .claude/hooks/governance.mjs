#!/usr/bin/env node
// governance.mjs — PreToolUse hook: hard-gate на ПРАВКУ файла (Edit/Write/NotebookEdit)
// ВНЕ зоны owner'а. Наш дифференциатор: границу зоны держит МАШИННЫЙ гейт по
// файловому пути, а не текст промпта (промпт-граница ненадёжна — инъекция сбивает).
// Родственник git-gate.mjs: git-gate режет git-ЗАПИСЬ по роли, governance режет ПРАВКУ вне paths[].
//
// Доступ по роли:
//   - architect (scope=main, marker) → БЕЗ ограничения по путям (видит всё).
//   - owner (scope=<zone>)           → правки ТОЛЬКО внутри zonePaths(зоны) из harness.yaml.
//   - layer (scope=layer)            → своя политика: файлы не ограничиваем (узость — промптом; git=none).
//   - scope не резолвится в зону       → DENY всё (нет boundary → аномалия, safe-by-default).
//
// Субагенты НЕ триггерят SessionStart и наследуют env OMNIFIELD_SCOPE родителя →
// routine-помощник owner'а виден гейту как owner-зона и режется теми же paths[] (граница не течёт).
//
// Контракт (Claude Code PreToolUse):
//   stdin  = JSON { tool_name, tool_input:{ file_path | notebook_path }, session_id, cwd }
//   stdout = JSON { hookSpecificOutput:{ hookEventName, permissionDecision, permissionDecisionReason } }
//   exit 0 всегда; FAIL-OPEN на внутренней ошибке (правку не ломаем из-за бага гейта).

import { existsSync, readFileSync, realpathSync } from "node:fs";
import { basename, dirname, isAbsolute, join, relative, resolve } from "node:path";
import { argv } from "node:process";
import { fileURLToPath } from "node:url";
import { loadConfig, roleOf, zonePaths } from "./harness-config.mjs";

// Файло-мутирующие тулы Claude Code (MultiEdit — на случай старых сборок).
const EDIT_TOOLS = new Set(["Edit", "Write", "NotebookEdit", "MultiEdit"]);

export function isEditTool(toolName) {
  return EDIT_TOOLS.has(toolName);
}

/** Путь-цель правки из tool_input (Edit/Write=file_path, NotebookEdit=notebook_path). */
export function targetPath(input) {
  const ti = input?.tool_input ?? {};
  return ti.file_path ?? ti.notebook_path ?? null;
}

/**
 * Граница правок для scope: architect/layer → без ограничения; owner → корни zonePaths
 * (абсолютные, от repoRoot); нерезолвимая зона → ограничение с пустыми корнями (DENY всё).
 */
export function ownedRoots(scope, config, repoRoot) {
  const role = roleOf(scope);
  if (role === "architect" || role === "layer") return { unrestricted: true, roots: [] };
  // owner: зона должна резолвиться и иметь пути
  const zone = config?.zones?.[scope];
  const roots = zonePaths(zone).map((p) => resolve(repoRoot, p));
  return { unrestricted: false, roots };
}

/** true, если targetAbs === root или лежит ВНУТРИ него (по сегментам, без ложных префиксов). */
export function within(targetAbs, root) {
  if (targetAbs === root) return true;
  const rel = relative(root, targetAbs);
  return rel !== "" && !rel.startsWith("..") && !isAbsolute(rel);
}

/**
 * Резолв цели в абсолютный путь с защитой от обхода: `..` сворачивается resolve'ом; симлинки —
 * realpath ближайшего существующего предка (файл может ещё не существовать при Write) + хвост.
 */
export function resolveTarget(rawPath, repoRoot) {
  const abs = resolve(repoRoot, rawPath);
  // realpath ближайшего существующего предка → ловит симлинк-папку, ведущую наружу зоны.
  let anc = abs;
  const tail = [];
  while (!existsSync(anc)) {
    const parent = dirname(anc);
    if (parent === anc) break; // дошли до корня FS
    tail.unshift(basename(anc)); // basename корректен и на руте (`/repo` → `repo`, не `epo`)
    anc = parent;
  }
  try {
    const real = realpathSync(anc);
    return tail.length ? join(real, ...tail) : real;
  } catch {
    return abs; // realpath упал — берём resolve-путь (fail-safe к строгой проверке ниже)
  }
}

/**
 * Каталог ПАМЯТИ роль-сессии — её собственный инструмент, а не чужая зона.
 *
 * Заведено 2026-08-22 после случая с owner-`control`: он не смог поправить свою же заметку
 * («порчу ставить только после коммита»), потому что каталог памяти лежит ВНЕ репозитория, и
 * гейт видел его как «чужое». Правило рамки при этом ничего такого не запрещает — наоборот,
 * оно прямо разрешает держать в памяти контекст текущей работы.
 *
 * Граница узкая намеренно: разрешается ровно сегмент `memory` внутри `claude/projects/…`.
 * **Это НЕ дыра в границе зон:** durable-знание продукта по-прежнему живёт на витрине, и
 * память его не заменяет — она про то, как агент работает, а не про то, как устроен мир.
 * Правку кода чужой зоны через этот путь не сделать: репозиторий здесь ни при чём.
 */
export function isOwnMemory(targetAbs, repoRoot) {
  // ВНУТРИ репозитория памяти не бывает. Без этой проверки владелец зоны заводил бы у себя
  // папку `claude/projects/x/memory/` и писал в неё что угодно — гейт пустил бы по имени пути.
  // Найдено собственной порчей при заведении правила: первая редакция такой путь РАЗРЕШАЛА.
  if (within(targetAbs, repoRoot)) return false;
  const parts = targetAbs.split(/[/\\]/);
  const projects = parts.indexOf("projects");
  if (projects < 1 || parts[projects - 1] !== "claude") return false;
  return parts.slice(projects + 1).includes("memory");
}

/**
 * Причина блокировки правки, либо null (разрешено). unrestricted → всегда null; иначе цель
 * должна лежать хотя бы в одном owned-корне либо быть памятью самой роли. Пустые корни
 * (нерезолвимая зона) → всё блокируется.
 */
export function editViolation({ scope, config, repoRoot, rawPath }) {
  const owned = ownedRoots(scope, config, repoRoot);
  if (owned.unrestricted) return null;
  const target = resolveTarget(rawPath, repoRoot);
  if (isOwnMemory(target, repoRoot)) return null;
  if (owned.roots.some((r) => within(target, r))) return null;
  if (!owned.roots.length) {
    return `scope "${scope}" не резолвится в зону с путями — boundary неизвестна`;
  }
  const rel = relative(repoRoot, target) || target;
  return `файл \`${rel}\` вне зоны owner-${scope} (${owned.roots.map((r) => `${relative(repoRoot, r)}/`).join(", ")})`;
}

function isMainSession(input) {
  const sessionId = input?.session_id;
  if (!sessionId) return false;
  const cwd = input.cwd || process.cwd();
  try {
    const ids = readFileSync(join(cwd, ".claude", ".main-session-id"), "utf8")
      .split(/\r?\n/)
      .map((l) => l.trim())
      .filter(Boolean);
    return ids.includes(String(sessionId));
  } catch {
    return false;
  }
}

function allow() {
  process.stdout.write(
    JSON.stringify({
      hookSpecificOutput: { hookEventName: "PreToolUse", permissionDecision: "allow" },
    }),
  );
  process.exit(0);
}

function deny(reason) {
  const msg = [
    `❌ Правка заблокирована harness-хуком (governance).`,
    ``,
    `Причина: ${reason}.`,
    ``,
    `Ты owner — правишь ТОЛЬКО свою зону (paths[] из \`.claude/harness.yaml\`). Файл вне неё,`,
    `в т.ч. cross-zone/контракт — это НЕ твоя граница.`,
    ``,
    `Действие: STOP. Не обходи (симлинк / относительный путь — гейт резолвит реальный путь).`,
    `Эскалация ВВЕРХ: верни state architect с описанием, что и зачем нужно за границей.`,
  ].join("\n");
  process.stdout.write(
    JSON.stringify({
      hookSpecificOutput: {
        hookEventName: "PreToolUse",
        permissionDecision: "deny",
        permissionDecisionReason: msg,
      },
    }),
  );
  process.exit(0);
}

function main() {
  let input;
  try {
    input = JSON.parse(readFileSync(0, "utf8").replace(/^﻿/, ""));
  } catch {
    return allow();
  }
  if (!isEditTool(input.tool_name)) return allow();
  const rawPath = targetPath(input);
  if (!rawPath) return allow(); // нечего проверять
  if (isMainSession(input)) return allow(); // architect (marker) — без ограничения

  const repoRoot = input.cwd || process.cwd();
  const scope = process.env.OMNIFIELD_SCOPE;
  // Нет scope у не-main сессии — не можем определить зону → safe-by-default DENY.
  if (!scope) return deny(`OMNIFIELD_SCOPE не задан — зона (boundary) неизвестна`);

  const config = loadConfig(repoRoot);
  const reason = editViolation({ scope, config, repoRoot, rawPath });
  if (!reason) return allow();
  deny(reason);
}

// main() ТОЛЬКО как скрипт (читает stdin(0)) — при import (тесты) не запускается.
if (fileURLToPath(import.meta.url) === argv[1]) {
  try {
    main();
  } catch {
    allow(); // FAIL-OPEN
  }
}
