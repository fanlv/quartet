// Condition-expression model shared by the If-Else (condition) and loop
// "until" (untilCondition) visual builders.
//
// The single source of truth on the wire is still the condition STRING stored
// in GraphNodeConfig.condition / .untilCondition; the backend validates it with
// services/graph.ParseCondition. This module only converts between that string
// and a flat, builder-friendly shape — it deliberately mirrors the backend
// grammar so a round-trip never changes meaning:
//
//   comparison := {{var}} OP operand option*
//   operand    := {{var}} | "literal"
//   OP         := == | != | > | >= | < | <= | StartWith | EndWith
//   option     := 忽略大小写 | 忽略空格
//   joined by a SINGLE connector: 且 (all) or 或 (any)
//
// Anything the flat builder cannot represent losslessly — parentheses, 非
// (NOT), mixed 且/或, a literal on the left, or per-rule option mismatch — makes
// tryParseSimple return null so the caller falls back to the raw-text "advanced"
// editor and the user's expression is never silently rewritten.

export type CondOp = '==' | '!=' | '>' | '>=' | '<' | '<=' | 'StartWith' | 'EndWith';

// All eight operators the backend accepts, in builder display order.
export const COND_OPS: CondOp[] = ['==', '!=', '>', '>=', '<', '<=', 'StartWith', 'EndWith'];

export type CondJoin = '且' | '或';

export const OPT_IGNORE_CASE = '忽略大小写';
export const OPT_IGNORE_SPACE = '忽略空格';

export interface CondRule {
  // Left operand is always a variable name (without the {{ }} braces).
  leftVar: string;
  op: CondOp;
  // Right operand is a string literal by default, or a variable when rightIsVar.
  rightIsVar: boolean;
  rightValue: string;
}

export interface SimpleCondition {
  join: CondJoin;
  rules: CondRule[];
  ignoreCase: boolean;
  ignoreSpace: boolean;
}

const VAR_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

// Mirrors services/graph.isValidVarName.
export function isValidVarName(name: string): boolean {
  return VAR_NAME_RE.test(name);
}

export function emptySimpleCondition(): SimpleCondition {
  return {
    join: '且',
    rules: [{ leftVar: '', op: '==', rightIsVar: false, rightValue: '' }],
    ignoreCase: false,
    ignoreSpace: false,
  };
}

// Escape a string literal the way the backend's scanString unescapes it: only
// \\ and \" are legal escapes, so backslash MUST be escaped before quote.
function escapeLiteral(s: string): string {
  return s.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
}

function serializeOperand(value: string, isVar: boolean): string {
  return isVar ? `{{${value}}}` : `"${escapeLiteral(value)}"`;
}

// Build the canonical condition string from a flat builder state. The result is
// parseable by the backend whenever every leftVar is a valid variable name.
export function serializeCondition(cond: SimpleCondition): string {
  const optSuffix =
    (cond.ignoreCase ? ` ${OPT_IGNORE_CASE}` : '') + (cond.ignoreSpace ? ` ${OPT_IGNORE_SPACE}` : '');
  const parts = cond.rules.map((r) => {
    const left = `{{${r.leftVar}}}`;
    const right = serializeOperand(r.rightValue, r.rightIsVar);
    return `${left} ${r.op} ${right}${optSuffix}`;
  });
  return parts.join(` ${cond.join} `);
}

// True once every rule has a valid left variable name and a non-empty right
// variable when comparing against a variable. Used to decide whether the live
// preview / save is meaningful; the backend remains the final validator.
export function isSimpleConditionComplete(cond: SimpleCondition): boolean {
  if (cond.rules.length === 0) return false;
  return cond.rules.every(
    (r) => isValidVarName(r.leftVar) && (!r.rightIsVar || isValidVarName(r.rightValue)),
  );
}

// ---- tokenizer (faithful subset of services/graph.tokenizeCondition) ----

type Tok =
  | { k: 'var'; v: string }
  | { k: 'str'; v: string }
  | { k: 'op'; v: CondOp }
  | { k: 'and' }
  | { k: 'or' }
  | { k: 'not' }
  | { k: 'lparen' }
  | { k: 'rparen' }
  | { k: 'optCase' }
  | { k: 'optSpace' };

const COMPARE_OPS = ['==', '!=', '>=', '<=', '>', '<'];

function isAsciiLetter(ch: string): boolean {
  return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z');
}

// Returns null on ANY lexical problem — the caller treats that as "cannot be
// represented simply" and falls back to advanced mode rather than throwing.
function tokenize(expr: string): Tok[] | null {
  const rs = Array.from(expr);
  const n = rs.length;
  const toks: Tok[] = [];
  let i = 0;
  while (i < n) {
    const r = rs[i];
    if (/\s/.test(r)) {
      i++;
      continue;
    }
    if (r === '(') {
      toks.push({ k: 'lparen' });
      i++;
      continue;
    }
    if (r === ')') {
      toks.push({ k: 'rparen' });
      i++;
      continue;
    }
    if (r === '{') {
      if (rs[i + 1] !== '{') return null;
      let j = i + 2;
      let name = '';
      let closed = false;
      while (j < n) {
        if (rs[j] === '}') {
          if (rs[j + 1] === '}') {
            closed = true;
            break;
          }
          return null;
        }
        name += rs[j];
        j++;
      }
      if (!closed || !isValidVarName(name)) return null;
      toks.push({ k: 'var', v: name });
      i = j + 2;
      continue;
    }
    if (r === '"') {
      let j = i + 1;
      let val = '';
      let closed = false;
      while (j < n) {
        const c = rs[j];
        if (c === '"') {
          closed = true;
          break;
        }
        if (c === '\n' || c === '\r') return null;
        if (c === '\\') {
          const esc = rs[j + 1];
          if (esc === '"') val += '"';
          else if (esc === '\\') val += '\\';
          else return null;
          j += 2;
          continue;
        }
        val += c;
        j++;
      }
      if (!closed) return null;
      toks.push({ k: 'str', v: val });
      i = j + 1;
      continue;
    }
    if (r === '=' || r === '!' || r === '>' || r === '<') {
      const rest = rs.slice(i).join('');
      const op = COMPARE_OPS.find((o) => rest.startsWith(o));
      if (!op) return null;
      toks.push({ k: 'op', v: op as CondOp });
      i += op.length;
      continue;
    }
    if (r === '且') {
      toks.push({ k: 'and' });
      i++;
      continue;
    }
    if (r === '或') {
      toks.push({ k: 'or' });
      i++;
      continue;
    }
    if (r === '非') {
      toks.push({ k: 'not' });
      i++;
      continue;
    }
    if (rs.slice(i, i + OPT_IGNORE_CASE.length).join('') === OPT_IGNORE_CASE) {
      toks.push({ k: 'optCase' });
      i += OPT_IGNORE_CASE.length;
      continue;
    }
    if (rs.slice(i, i + OPT_IGNORE_SPACE.length).join('') === OPT_IGNORE_SPACE) {
      toks.push({ k: 'optSpace' });
      i += OPT_IGNORE_SPACE.length;
      continue;
    }
    if (isAsciiLetter(r)) {
      let j = i;
      while (j < n && isAsciiLetter(rs[j])) j++;
      const word = rs.slice(i, j).join('');
      if (word !== 'StartWith' && word !== 'EndWith') return null;
      toks.push({ k: 'op', v: word as CondOp });
      i = j;
      continue;
    }
    return null;
  }
  return toks;
}

// Parse a single comparison `{{var}} OP operand option*` starting at index `i`.
// Returns the rule, its per-rule option flags, and the next index — or null.
function parseComparison(
  toks: Tok[],
  i: number,
): { rule: CondRule; ignoreCase: boolean; ignoreSpace: boolean; next: number } | null {
  const left = toks[i];
  // The flat builder only models a variable on the left.
  if (!left || left.k !== 'var') return null;
  const opTok = toks[i + 1];
  if (!opTok || opTok.k !== 'op') return null;
  const right = toks[i + 2];
  if (!right || (right.k !== 'var' && right.k !== 'str')) return null;
  let j = i + 3;
  let ignoreCase = false;
  let ignoreSpace = false;
  while (toks[j] && (toks[j].k === 'optCase' || toks[j].k === 'optSpace')) {
    if (toks[j].k === 'optCase') ignoreCase = true;
    else ignoreSpace = true;
    j++;
  }
  return {
    rule: {
      leftVar: left.v,
      op: opTok.v,
      rightIsVar: right.k === 'var',
      rightValue: right.v,
    },
    ignoreCase,
    ignoreSpace,
    next: j,
  };
}

// Try to read `expr` back into the flat builder shape. Returns null when the
// expression uses anything the builder can't round-trip losslessly, so the
// caller keeps the raw text in advanced mode.
export function tryParseSimple(expr: string): SimpleCondition | null {
  const trimmed = expr.trim();
  if (trimmed === '') return null;
  const toks = tokenize(trimmed);
  if (!toks) return null;
  // Reject anything outside the flat grammar up front.
  if (toks.some((t) => t.k === 'lparen' || t.k === 'rparen' || t.k === 'not')) return null;

  const rules: CondRule[] = [];
  const optFlags: Array<{ ignoreCase: boolean; ignoreSpace: boolean }> = [];
  let join: CondJoin | null = null;
  let i = 0;
  while (i < toks.length) {
    const parsed = parseComparison(toks, i);
    if (!parsed) return null;
    rules.push(parsed.rule);
    optFlags.push({ ignoreCase: parsed.ignoreCase, ignoreSpace: parsed.ignoreSpace });
    i = parsed.next;
    if (i >= toks.length) break;
    const conn = toks[i];
    if (conn.k !== 'and' && conn.k !== 'or') return null;
    const thisJoin: CondJoin = conn.k === 'and' ? '且' : '或';
    // A single connector type only — mixed 且/或 needs explicit grouping.
    if (join !== null && join !== thisJoin) return null;
    join = thisJoin;
    i++;
    if (i >= toks.length) return null; // trailing connector
  }
  if (rules.length === 0) return null;

  // The builder exposes one global pair of option toggles, so every rule must
  // carry the same flags; otherwise we can't represent it without changing
  // meaning.
  const first = optFlags[0];
  if (!optFlags.every((f) => f.ignoreCase === first.ignoreCase && f.ignoreSpace === first.ignoreSpace)) {
    return null;
  }

  return {
    join: join ?? '且',
    rules,
    ignoreCase: first.ignoreCase,
    ignoreSpace: first.ignoreSpace,
  };
}
