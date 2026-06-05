import { useRef, useMemo } from 'react';
import './ShellEditor.css';

interface ShellEditorProps {
  value: string;
  onChange?: (value: string) => void;
  placeholder?: string;
  readOnly?: boolean;
}

// Shell keywords
const KEYWORDS = new Set([
  'if', 'then', 'else', 'elif', 'fi', 'for', 'while', 'do', 'done',
  'case', 'esac', 'in', 'function', 'select', 'until', 'return',
  'break', 'continue', 'local', 'export', 'readonly', 'declare',
  'typeset', 'unset', 'shift', 'trap', 'eval', 'exec', 'source',
]);

// Common shell builtins/commands
const BUILTINS = new Set([
  'echo', 'printf', 'cd', 'pwd', 'ls', 'cp', 'mv', 'rm', 'mkdir',
  'rmdir', 'cat', 'grep', 'sed', 'awk', 'find', 'xargs', 'sort',
  'uniq', 'wc', 'head', 'tail', 'cut', 'tr', 'tee', 'chmod',
  'chown', 'curl', 'wget', 'tar', 'gzip', 'gunzip', 'zip', 'unzip',
  'ssh', 'scp', 'rsync', 'git', 'docker', 'npm', 'node', 'python',
  'pip', 'make', 'sudo', 'apt', 'yum', 'brew', 'sleep', 'kill',
  'ps', 'top', 'df', 'du', 'date', 'touch', 'test', 'read', 'set',
]);

function escapeHtml(str: string): string {
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function highlightShell(code: string): string {
  const lines = code.split('\n');
  return lines.map((line) => highlightLine(line)).join('\n');
}

function highlightLine(line: string): string {
  const result: string[] = [];
  let i = 0;

  // Leading whitespace
  while (i < line.length && (line[i] === ' ' || line[i] === '\t')) {
    result.push(line[i]);
    i++;
  }

  // Full-line comment
  if (line[i] === '#') {
    result.push(`<span class="sh-comment">${escapeHtml(line.slice(i))}</span>`);
    return result.join('');
  }

  // Track if we've seen the first word (command position)
  let isCommandPosition = true;

  while (i < line.length) {
    const ch = line[i];

    // Comment (not inside quotes)
    if (ch === '#') {
      result.push(`<span class="sh-comment">${escapeHtml(line.slice(i))}</span>`);
      break;
    }

    // Double-quoted string
    if (ch === '"') {
      const end = findClosingQuote(line, i, '"');
      const str = line.slice(i, end + 1);
      result.push(`<span class="sh-string">${highlightStringContent(str)}</span>`);
      i = end + 1;
      isCommandPosition = false;
      continue;
    }

    // Single-quoted string
    if (ch === "'") {
      const end = findClosingQuote(line, i, "'");
      const str = line.slice(i, end + 1);
      result.push(`<span class="sh-string">${escapeHtml(str)}</span>`);
      i = end + 1;
      isCommandPosition = false;
      continue;
    }

    // Backtick
    if (ch === '`') {
      const end = findClosingQuote(line, i, '`');
      const str = line.slice(i, end + 1);
      result.push(`<span class="sh-subshell">${escapeHtml(str)}</span>`);
      i = end + 1;
      isCommandPosition = false;
      continue;
    }

    // $(...) or ${...} or $VAR
    if (ch === '$') {
      const varResult = parseVariable(line, i);
      result.push(`<span class="sh-variable">${escapeHtml(varResult.text)}</span>`);
      i = varResult.end;
      isCommandPosition = false;
      continue;
    }

    // {{...}} template variable
    if (ch === '{' && line[i + 1] === '{') {
      const closeIdx = line.indexOf('}}', i + 2);
      if (closeIdx !== -1) {
        const tpl = line.slice(i, closeIdx + 2);
        result.push(`<span class="sh-template">${escapeHtml(tpl)}</span>`);
        i = closeIdx + 2;
        isCommandPosition = false;
        continue;
      }
    }

    // Operators: |, &&, ||, ;, >, >>, <, &
    if ('|&;><'.includes(ch)) {
      let op = ch;
      if (i + 1 < line.length) {
        const next = line[i + 1];
        if ((ch === '|' && next === '|') || (ch === '&' && next === '&') ||
            (ch === '>' && next === '>') || (ch === '<' && next === '<')) {
          op = ch + next;
        }
      }
      result.push(`<span class="sh-operator">${escapeHtml(op)}</span>`);
      i += op.length;
      isCommandPosition = true;
      continue;
    }

    // Numbers (standalone)
    if (/[0-9]/.test(ch) && (i === 0 || /[\s;|&><=]/.test(line[i - 1]))) {
      let num = '';
      let j = i;
      while (j < line.length && /[0-9.]/.test(line[j])) {
        num += line[j];
        j++;
      }
      if (j >= line.length || /[\s;|&><=)#]/.test(line[j])) {
        result.push(`<span class="sh-number">${escapeHtml(num)}</span>`);
        i = j;
        isCommandPosition = false;
        continue;
      }
    }

    // Words
    if (/[a-zA-Z_\-/.]/.test(ch)) {
      let word = '';
      let j = i;
      while (j < line.length && /[a-zA-Z0-9_\-/.+@:]/.test(line[j])) {
        word += line[j];
        j++;
      }

      // Check for assignment (VAR=)
      if (j < line.length && line[j] === '=' && /^[a-zA-Z_][a-zA-Z0-9_]*$/.test(word)) {
        result.push(`<span class="sh-variable">${escapeHtml(word)}</span>`);
        result.push(`<span class="sh-operator">=</span>`);
        i = j + 1;
        isCommandPosition = false;
        continue;
      }

      if (KEYWORDS.has(word)) {
        result.push(`<span class="sh-keyword">${escapeHtml(word)}</span>`);
        isCommandPosition = true;
      } else if (isCommandPosition && BUILTINS.has(word)) {
        result.push(`<span class="sh-builtin">${escapeHtml(word)}</span>`);
        isCommandPosition = false;
      } else if (isCommandPosition) {
        result.push(`<span class="sh-command">${escapeHtml(word)}</span>`);
        isCommandPosition = false;
      } else if (word.startsWith('-')) {
        result.push(`<span class="sh-flag">${escapeHtml(word)}</span>`);
      } else {
        result.push(escapeHtml(word));
      }
      i = j;
      continue;
    }

    // Whitespace
    if (ch === ' ' || ch === '\t') {
      result.push(ch);
      i++;
      continue;
    }

    // Other characters
    result.push(escapeHtml(ch));
    i++;
    isCommandPosition = false;
  }

  return result.join('');
}

function findClosingQuote(line: string, start: number, quote: string): number {
  for (let i = start + 1; i < line.length; i++) {
    if (line[i] === '\\' && quote !== "'") {
      i++; // skip escaped char
      continue;
    }
    if (line[i] === quote) return i;
  }
  return line.length - 1; // unclosed
}

function highlightStringContent(str: string): string {
  // Highlight $VAR inside double-quoted strings
  return escapeHtml(str).replace(
    /(\$\{[^}]*\}|\$[a-zA-Z_][a-zA-Z0-9_]*|\$[0-9#?@!*-])/g,
    '<span class="sh-variable">$1</span>'
  );
}

function parseVariable(line: string, start: number): { text: string; end: number } {
  const i = start + 1;
  if (i >= line.length) return { text: '$', end: i };

  if (line[i] === '(') {
    // $(...) - find matching paren
    let depth = 1;
    let j = i + 1;
    while (j < line.length && depth > 0) {
      if (line[j] === '(') depth++;
      else if (line[j] === ')') depth--;
      j++;
    }
    return { text: line.slice(start, j), end: j };
  }

  if (line[i] === '{') {
    const close = line.indexOf('}', i);
    if (close !== -1) return { text: line.slice(start, close + 1), end: close + 1 };
    return { text: line.slice(start, i + 1), end: i + 1 };
  }

  // $VAR or $1, $#, $?, etc.
  if (/[a-zA-Z_]/.test(line[i])) {
    let j = i;
    while (j < line.length && /[a-zA-Z0-9_]/.test(line[j])) j++;
    return { text: line.slice(start, j), end: j };
  }

  if (/[0-9#?@!*-]/.test(line[i])) {
    return { text: line.slice(start, i + 1), end: i + 1 };
  }

  return { text: '$', end: i };
}

export function ShellEditor({ value, onChange, placeholder, readOnly }: ShellEditorProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const highlightRef = useRef<HTMLPreElement>(null);
  const lineNumbersRef = useRef<HTMLDivElement>(null);
  const lineCount = Math.max((value || '').split('\n').length, 1);

  const highlighted = useMemo(() => highlightShell(value || ''), [value]);

  const syncScroll = () => {
    const textarea = textareaRef.current;
    if (textarea && lineNumbersRef.current) {
      lineNumbersRef.current.scrollTop = textarea.scrollTop;
    }
    if (textarea && highlightRef.current) {
      highlightRef.current.scrollTop = textarea.scrollTop;
      highlightRef.current.scrollLeft = textarea.scrollLeft;
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Tab' && !readOnly && onChange) {
      e.preventDefault();
      const textarea = textareaRef.current;
      if (!textarea) return;
      const start = textarea.selectionStart;
      const end = textarea.selectionEnd;
      const newValue = value.substring(0, start) + '  ' + value.substring(end);
      onChange(newValue);
      requestAnimationFrame(() => {
        textarea.selectionStart = textarea.selectionEnd = start + 2;
      });
    }
  };

  return (
    <div className="shell-editor">
      <div className="shell-editor-body">
        <div className="shell-editor-line-numbers" ref={lineNumbersRef}>
          {Array.from({ length: lineCount }, (_, i) => (
            <div key={i + 1} className="shell-editor-line-number">
              {i + 1}
            </div>
          ))}
        </div>
        <div className="shell-editor-code-area">
          <pre
            ref={highlightRef}
            className="shell-editor-highlight"
            aria-hidden="true"
            dangerouslySetInnerHTML={{ __html: highlighted + '\n' }}
          />
          <textarea
            ref={textareaRef}
            className="shell-editor-textarea"
            value={value}
            onChange={(e) => onChange?.(e.target.value)}
            onScroll={syncScroll}
            onKeyDown={handleKeyDown}
            placeholder={placeholder}
            readOnly={readOnly}
            spellCheck={false}
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="off"
          />
        </div>
      </div>
    </div>
  );
}
