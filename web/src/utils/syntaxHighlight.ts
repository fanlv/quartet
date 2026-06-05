/**
 * Lightweight syntax highlighting engine for the file viewer.
 * Operates line-by-line using regex-based tokenization.
 */

export type TokenType =
  | 'keyword'
  | 'string'
  | 'comment'
  | 'number'
  | 'operator'
  | 'function'
  | 'type'
  | 'punctuation'
  | 'builtin'
  | 'variable'
  | 'tag'
  | 'attribute'
  | 'property'
  | 'decorator'
  | 'regexp';

export interface Token {
  type: TokenType | null; // null = plain text
  value: string;
}

interface LanguageRule {
  pattern: RegExp;
  type: TokenType;
}

interface LanguageDef {
  rules: LanguageRule[];
}

// --- Language definitions ---

const goKeywords = 'break|case|chan|const|continue|default|defer|else|fallthrough|for|func|go|goto|if|import|interface|map|package|range|return|select|struct|switch|type|var';
const goBuiltins = 'append|cap|close|complex|copy|delete|imag|len|make|new|panic|print|println|real|recover';
const goTypes = 'bool|byte|complex64|complex128|error|float32|float64|int|int8|int16|int32|int64|rune|string|uint|uint8|uint16|uint32|uint64|uintptr|any';

const jsKeywords = 'abstract|arguments|async|await|break|case|catch|class|const|continue|debugger|default|delete|do|else|enum|export|extends|finally|for|from|function|get|if|implements|import|in|instanceof|interface|let|new|of|package|private|protected|public|return|set|static|super|switch|this|throw|try|typeof|var|void|while|with|yield';
const jsBuiltins = 'Array|Boolean|Date|Error|Function|JSON|Map|Math|Number|Object|Promise|Proxy|Reflect|RegExp|Set|String|Symbol|WeakMap|WeakSet|console|document|globalThis|module|navigator|process|require|undefined|window';

const pyKeywords = 'False|None|True|and|as|assert|async|await|break|class|continue|def|del|elif|else|except|finally|for|from|global|if|import|in|is|lambda|nonlocal|not|or|pass|raise|return|try|while|with|yield';
const pyBuiltins = 'abs|all|any|bin|bool|bytes|callable|chr|classmethod|compile|complex|delattr|dict|dir|divmod|enumerate|eval|exec|filter|float|format|frozenset|getattr|globals|hasattr|hash|help|hex|id|input|int|isinstance|issubclass|iter|len|list|locals|map|max|memoryview|min|next|object|oct|open|ord|pow|print|property|range|repr|reversed|round|set|setattr|slice|sorted|staticmethod|str|sum|super|tuple|type|vars|zip';

const rustKeywords = 'as|async|await|break|const|continue|crate|dyn|else|enum|extern|false|fn|for|if|impl|in|let|loop|match|mod|move|mut|pub|ref|return|self|Self|static|struct|super|trait|true|type|unsafe|use|where|while';
const rustTypes = 'bool|char|f32|f64|i8|i16|i32|i64|i128|isize|str|u8|u16|u32|u64|u128|usize|String|Vec|Option|Result|Box|Rc|Arc|Cell|RefCell|HashMap|HashSet|BTreeMap|BTreeSet';

const javaKeywords = 'abstract|assert|boolean|break|byte|case|catch|char|class|const|continue|default|do|double|else|enum|extends|final|finally|float|for|goto|if|implements|import|instanceof|int|interface|long|native|new|package|private|protected|public|return|short|static|strictfp|super|switch|synchronized|this|throw|throws|transient|try|void|volatile|while';

const cKeywords = 'auto|break|case|char|const|continue|default|do|double|else|enum|extern|float|for|goto|if|inline|int|long|register|restrict|return|short|signed|sizeof|static|struct|switch|typedef|union|unsigned|void|volatile|while|_Bool|_Complex|_Imaginary|bool|true|false|NULL|nullptr|class|namespace|template|typename|using|virtual|override|public|private|protected|new|delete|throw|try|catch|noexcept|constexpr|decltype|static_assert|alignas|alignof|thread_local|static_cast|dynamic_cast|const_cast|reinterpret_cast';

const shellKeywords = 'if|then|else|elif|fi|for|while|do|done|case|esac|in|function|return|local|export|declare|typeset|readonly|unset|shift|break|continue|exit|trap|source|alias|unalias|set|eval|exec';
const shellBuiltins = 'echo|printf|read|test|cd|pwd|ls|cp|mv|rm|mkdir|rmdir|touch|cat|grep|sed|awk|find|sort|uniq|wc|head|tail|tee|xargs|curl|wget|tar|gzip|gunzip|zip|unzip|ssh|scp|git|docker|make|npm|yarn|pip|python|node|go|cargo|rustc|java|javac|gcc|g\\+\\+|clang';

const sqlKeywords = 'SELECT|FROM|WHERE|INSERT|INTO|VALUES|UPDATE|SET|DELETE|CREATE|DROP|ALTER|TABLE|INDEX|VIEW|JOIN|INNER|LEFT|RIGHT|OUTER|FULL|CROSS|ON|AND|OR|NOT|IN|EXISTS|BETWEEN|LIKE|IS|NULL|AS|ORDER|BY|GROUP|HAVING|LIMIT|OFFSET|UNION|ALL|DISTINCT|COUNT|SUM|AVG|MIN|MAX|CASE|WHEN|THEN|ELSE|END|PRIMARY|KEY|FOREIGN|REFERENCES|CONSTRAINT|DEFAULT|CHECK|UNIQUE';

const rubyKeywords = 'BEGIN|END|alias|and|begin|break|case|class|def|defined\\?|do|else|elsif|end|ensure|false|for|if|in|module|next|nil|not|or|redo|rescue|retry|return|self|super|then|true|undef|unless|until|when|while|yield';

const phpKeywords = 'abstract|and|array|as|break|callable|case|catch|class|clone|const|continue|declare|default|die|do|echo|else|elseif|empty|enddeclare|endfor|endforeach|endif|endswitch|endwhile|eval|exit|extends|final|finally|fn|for|foreach|function|global|goto|if|implements|include|include_once|instanceof|insteadof|interface|isset|list|match|namespace|new|or|print|private|protected|public|readonly|require|require_once|return|static|switch|throw|trait|try|unset|use|var|while|xor|yield';

function buildLang(opts: {
  keywords: string;
  builtins?: string;
  types?: string;
  singleLineComment?: string;
  multiLineComment?: [string, string];
  stringChars?: string[];
  templateString?: boolean;
  hashComment?: boolean;
  decorators?: boolean;
}): LanguageDef {
  const rules: LanguageRule[] = [];

  // Multi-line comment start (we handle the full comment in the tokenizer)
  if (opts.multiLineComment) {
    const [open] = opts.multiLineComment;
    const escaped = open.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    rules.push({ pattern: new RegExp(`${escaped}.*`), type: 'comment' });
  }

  // Single line comment
  if (opts.singleLineComment) {
    const escaped = opts.singleLineComment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    rules.push({ pattern: new RegExp(`${escaped}.*`), type: 'comment' });
  }
  if (opts.hashComment) {
    rules.push({ pattern: /#.*/, type: 'comment' });
  }

  // Decorators (Python, Java)
  if (opts.decorators) {
    rules.push({ pattern: /@[\w.]+/, type: 'decorator' });
  }

  // Strings
  if (opts.templateString) {
    rules.push({ pattern: /`(?:[^`\\]|\\.)*`/, type: 'string' });
  }
  const stringChars = opts.stringChars ?? ['"', "'"];
  for (const q of stringChars) {
    const escaped = q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    rules.push({ pattern: new RegExp(`${escaped}(?:[^${escaped}\\\\]|\\\\.)*${escaped}`), type: 'string' });
  }

  // Numbers
  rules.push({ pattern: /0[xX][0-9a-fA-F_]+/, type: 'number' });
  rules.push({ pattern: /0[bB][01_]+/, type: 'number' });
  rules.push({ pattern: /0[oO][0-7_]+/, type: 'number' });
  rules.push({ pattern: /\d[\d_]*\.[\d_]*(?:[eE][+-]?\d+)?/, type: 'number' });
  rules.push({ pattern: /\d[\d_]*(?:[eE][+-]?\d+)?/, type: 'number' });

  // Types
  if (opts.types) {
    rules.push({ pattern: new RegExp(`\\b(?:${opts.types})\\b`), type: 'type' });
  }

  // Builtins
  if (opts.builtins) {
    rules.push({ pattern: new RegExp(`\\b(?:${opts.builtins})\\b`), type: 'builtin' });
  }

  // Keywords
  rules.push({ pattern: new RegExp(`\\b(?:${opts.keywords})\\b`), type: 'keyword' });

  // Function calls
  rules.push({ pattern: /\b[a-zA-Z_]\w*(?=\s*\()/, type: 'function' });

  // Operators
  rules.push({ pattern: /[+\-*/%=!<>&|^~?:]+/, type: 'operator' });

  // Punctuation
  rules.push({ pattern: /[{}[\]();,.]/, type: 'punctuation' });

  return { rules };
}

const languages: Record<string, LanguageDef> = {
  go: buildLang({
    keywords: goKeywords,
    builtins: goBuiltins,
    types: goTypes,
    singleLineComment: '//',
    multiLineComment: ['/*', '*/'],
    stringChars: ['"', "'"],
    templateString: true,
  }),
  javascript: buildLang({
    keywords: jsKeywords,
    builtins: jsBuiltins,
    singleLineComment: '//',
    multiLineComment: ['/*', '*/'],
    templateString: true,
    decorators: true,
  }),
  typescript: buildLang({
    keywords: jsKeywords + '|type|interface|declare|namespace|module|readonly|keyof|infer|asserts|is|override|satisfies',
    builtins: jsBuiltins + '|never|unknown|any|void',
    types: 'string|number|boolean|symbol|bigint|null|undefined|object',
    singleLineComment: '//',
    multiLineComment: ['/*', '*/'],
    templateString: true,
    decorators: true,
  }),
  python: buildLang({
    keywords: pyKeywords,
    builtins: pyBuiltins,
    hashComment: true,
    stringChars: ['"', "'"],
    decorators: true,
  }),
  rust: buildLang({
    keywords: rustKeywords,
    types: rustTypes,
    singleLineComment: '//',
    multiLineComment: ['/*', '*/'],
    stringChars: ['"'],
  }),
  java: buildLang({
    keywords: javaKeywords,
    singleLineComment: '//',
    multiLineComment: ['/*', '*/'],
    decorators: true,
  }),
  c: buildLang({
    keywords: cKeywords,
    singleLineComment: '//',
    multiLineComment: ['/*', '*/'],
    stringChars: ['"', "'"],
  }),
  cpp: buildLang({
    keywords: cKeywords,
    singleLineComment: '//',
    multiLineComment: ['/*', '*/'],
    stringChars: ['"', "'"],
  }),
  shell: buildLang({
    keywords: shellKeywords,
    builtins: shellBuiltins,
    hashComment: true,
    stringChars: ['"', "'"],
  }),
  sql: buildLang({
    keywords: sqlKeywords,
    singleLineComment: '--',
    multiLineComment: ['/*', '*/'],
    stringChars: ["'"],
  }),
  ruby: buildLang({
    keywords: rubyKeywords,
    hashComment: true,
    stringChars: ['"', "'"],
  }),
  php: buildLang({
    keywords: phpKeywords,
    singleLineComment: '//',
    multiLineComment: ['/*', '*/'],
    hashComment: true,
    stringChars: ['"', "'"],
  }),
  css: {
    rules: [
      { pattern: /\/\*.*/, type: 'comment' },
      { pattern: /"(?:[^"\\]|\\.)*"/, type: 'string' },
      { pattern: /'(?:[^'\\]|\\.)*'/, type: 'string' },
      { pattern: /#[0-9a-fA-F]{3,8}\b/, type: 'number' },
      { pattern: /\d+\.?\d*(?:px|em|rem|%|vh|vw|vmin|vmax|ch|ex|cm|mm|in|pt|pc|s|ms|deg|rad|turn|fr)?/, type: 'number' },
      { pattern: /\b(?:inherit|initial|unset|revert|none|auto|normal|bold|italic|block|inline|flex|grid|absolute|relative|fixed|sticky|hidden|visible|scroll|solid|dashed|dotted|transparent|currentColor|important)\b/, type: 'keyword' },
      { pattern: /&|@[\w-]+/, type: 'keyword' },
      { pattern: /[.#][\w-]+/, type: 'function' },
      { pattern: /[\w-]+(?=\s*:)/, type: 'property' },
      { pattern: /[{}();:,]/, type: 'punctuation' },
    ],
  },
  html: {
    rules: [
      { pattern: /<!--.*/, type: 'comment' },
      { pattern: /"(?:[^"\\]|\\.)*"/, type: 'string' },
      { pattern: /'(?:[^'\\]|\\.)*'/, type: 'string' },
      { pattern: /<\/?[\w-]+/, type: 'tag' },
      { pattern: /\/?>/, type: 'tag' },
      { pattern: /\b[\w-]+(?==)/, type: 'attribute' },
      { pattern: /&\w+;/, type: 'keyword' },
    ],
  },
  json: {
    rules: [
      { pattern: /"(?:[^"\\]|\\.)*"\s*(?=:)/, type: 'property' },
      { pattern: /"(?:[^"\\]|\\.)*"/, type: 'string' },
      { pattern: /\b(?:true|false|null)\b/, type: 'keyword' },
      { pattern: /-?\d+\.?\d*(?:[eE][+-]?\d+)?/, type: 'number' },
      { pattern: /[{}[\],:]/, type: 'punctuation' },
    ],
  },
  yaml: {
    rules: [
      { pattern: /#.*/, type: 'comment' },
      { pattern: /"(?:[^"\\]|\\.)*"/, type: 'string' },
      { pattern: /'(?:[^'\\]|\\.)*'/, type: 'string' },
      { pattern: /\b(?:true|false|null|yes|no|on|off)\b/i, type: 'keyword' },
      { pattern: /-?\d+\.?\d*/, type: 'number' },
      { pattern: /[\w.-]+(?=\s*:)/, type: 'property' },
      { pattern: /[:\-|>]/, type: 'punctuation' },
    ],
  },
  markdown: {
    rules: [
      { pattern: /^#{1,6}\s+.*/, type: 'keyword' },
      { pattern: /\*\*[^*]+\*\*/, type: 'keyword' },
      { pattern: /\*[^*]+\*/, type: 'string' },
      { pattern: /`[^`]+`/, type: 'string' },
      { pattern: /^\s*[-*+]\s/, type: 'punctuation' },
      { pattern: /^\s*\d+\.\s/, type: 'punctuation' },
      { pattern: /\[([^\]]+)\]\([^)]+\)/, type: 'function' },
    ],
  },
  toml: {
    rules: [
      { pattern: /#.*/, type: 'comment' },
      { pattern: /"""[\s\S]*?"""/, type: 'string' },
      { pattern: /'''[\s\S]*?'''/, type: 'string' },
      { pattern: /"(?:[^"\\]|\\.)*"/, type: 'string' },
      { pattern: /'[^']*'/, type: 'string' },
      { pattern: /\b(?:true|false)\b/, type: 'keyword' },
      { pattern: /-?\d+\.?\d*/, type: 'number' },
      { pattern: /\[[\w.]+\]/, type: 'tag' },
      { pattern: /[\w.-]+(?=\s*=)/, type: 'property' },
      { pattern: /[=,[\]{}]/, type: 'punctuation' },
    ],
  },
  proto: buildLang({
    keywords: 'syntax|package|import|option|message|enum|service|rpc|returns|oneof|map|repeated|optional|required|reserved|extensions|extend|to|max|weak|public',
    types: 'double|float|int32|int64|uint32|uint64|sint32|sint64|fixed32|fixed64|sfixed32|sfixed64|bool|string|bytes',
    singleLineComment: '//',
    multiLineComment: ['/*', '*/'],
    stringChars: ['"', "'"],
  }),
  dockerfile: {
    rules: [
      { pattern: /#.*/, type: 'comment' },
      { pattern: /\b(?:FROM|RUN|CMD|LABEL|MAINTAINER|EXPOSE|ENV|ADD|COPY|ENTRYPOINT|VOLUME|USER|WORKDIR|ARG|ONBUILD|STOPSIGNAL|HEALTHCHECK|SHELL|AS)\b/i, type: 'keyword' },
      { pattern: /"(?:[^"\\]|\\.)*"/, type: 'string' },
      { pattern: /'(?:[^'\\]|\\.)*'/, type: 'string' },
      { pattern: /\$\{?\w+\}?/, type: 'variable' },
    ],
  },
};

// Aliases
languages.js = languages.javascript;
languages.jsx = languages.javascript;
languages.ts = languages.typescript;
languages.tsx = languages.typescript;
languages.py = languages.python;
languages.rs = languages.rust;
languages.sh = languages.shell;
languages.bash = languages.shell;
languages.zsh = languages.shell;
languages.h = languages.c;
languages.hpp = languages.cpp;
languages.cc = languages.cpp;
languages.cxx = languages.cpp;
languages.m = languages.c;
languages.mm = languages.cpp;
languages.kt = languages.java;
languages.kotlin = languages.java;
languages.scala = languages.java;
languages.groovy = languages.java;
languages.swift = languages.java;
languages.cs = languages.java;
languages.rb = languages.ruby;
languages.yml = languages.yaml;
languages.htm = languages.html;
languages.xml = languages.html;
languages.svg = languages.html;
languages.vue = languages.html;
languages.md = languages.markdown;
languages.mdx = languages.markdown;
languages.makefile = languages.shell;
languages.mk = languages.shell;

const extToLang: Record<string, string> = {
  go: 'go',
  js: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
  ts: 'typescript', tsx: 'typescript', mts: 'typescript', cts: 'typescript',
  py: 'python', pyw: 'python', pyi: 'python',
  rs: 'rust',
  java: 'java', kt: 'java', kts: 'java', scala: 'java', groovy: 'java', swift: 'java', cs: 'java',
  c: 'c', h: 'c', m: 'c',
  cpp: 'cpp', cc: 'cpp', cxx: 'cpp', hpp: 'cpp', hh: 'cpp', hxx: 'cpp', mm: 'cpp',
  sh: 'shell', bash: 'shell', zsh: 'shell', fish: 'shell',
  sql: 'sql',
  rb: 'ruby', rake: 'ruby', gemspec: 'ruby',
  php: 'php',
  css: 'css', scss: 'css', sass: 'css', less: 'css',
  html: 'html', htm: 'html', xml: 'html', svg: 'html', vue: 'html',
  json: 'json', jsonc: 'json', json5: 'json',
  yaml: 'yaml', yml: 'yaml',
  md: 'markdown', mdx: 'markdown',
  toml: 'toml',
  proto: 'proto',
  dockerfile: 'dockerfile',
};

// Special filenames
const filenameToLang: Record<string, string> = {
  Makefile: 'shell',
  Dockerfile: 'dockerfile',
  Jenkinsfile: 'groovy',
  Rakefile: 'ruby',
  Gemfile: 'ruby',
  Vagrantfile: 'ruby',
  '.gitignore': 'shell',
  '.dockerignore': 'shell',
  '.env': 'shell',
  '.bashrc': 'shell',
  '.zshrc': 'shell',
  '.profile': 'shell',
};

export function detectLanguage(filePath: string): string | null {
  const basename = filePath.split('/').pop() || '';

  // Check special filenames
  if (filenameToLang[basename]) return filenameToLang[basename];

  // Check extension
  const dotIdx = basename.lastIndexOf('.');
  if (dotIdx >= 0) {
    const ext = basename.slice(dotIdx + 1).toLowerCase();
    if (extToLang[ext]) return extToLang[ext];
  }

  return null;
}

export function getLanguageLabel(filePath: string): string {
  const lang = detectLanguage(filePath);
  if (!lang) {
    const ext = filePath.split('.').pop()?.toUpperCase();
    return ext || 'TEXT';
  }
  const labelMap: Record<string, string> = {
    go: 'Go',
    javascript: 'JavaScript',
    typescript: 'TypeScript',
    python: 'Python',
    rust: 'Rust',
    java: 'Java',
    c: 'C',
    cpp: 'C++',
    shell: 'Shell',
    sql: 'SQL',
    ruby: 'Ruby',
    php: 'PHP',
    css: 'CSS',
    html: 'HTML',
    json: 'JSON',
    yaml: 'YAML',
    markdown: 'Markdown',
    toml: 'TOML',
    proto: 'Protobuf',
    dockerfile: 'Dockerfile',
  };
  return labelMap[lang] || lang.toUpperCase();
}

/**
 * Tokenize a single line of code.
 */
export function tokenizeLine(line: string, lang: string | null): Token[] {
  if (!lang || !languages[lang]) {
    return [{ type: null, value: line }];
  }

  const langDef = languages[lang];
  const tokens: Token[] = [];
  let pos = 0;

  while (pos < line.length) {
    // Skip whitespace
    if (line[pos] === ' ' || line[pos] === '\t') {
      let end = pos + 1;
      while (end < line.length && (line[end] === ' ' || line[end] === '\t')) end++;
      tokens.push({ type: null, value: line.slice(pos, end) });
      pos = end;
      continue;
    }

    let matched = false;
    const rest = line.slice(pos);

    for (const rule of langDef.rules) {
      const m = rest.match(rule.pattern);
      if (m && m.index === 0 && m[0].length > 0) {
        tokens.push({ type: rule.type, value: m[0] });
        pos += m[0].length;
        matched = true;
        break;
      }
    }

    if (!matched) {
      // Accumulate plain text
      let end = pos + 1;
      while (end < line.length) {
        if (line[end] === ' ' || line[end] === '\t') break;
        const nextRest = line.slice(end);
        let foundRule = false;
        for (const rule of langDef.rules) {
          const m = nextRest.match(rule.pattern);
          if (m && m.index === 0 && m[0].length > 0) {
            foundRule = true;
            break;
          }
        }
        if (foundRule) break;
        end++;
      }
      tokens.push({ type: null, value: line.slice(pos, end) });
      pos = end;
    }
  }

  return tokens;
}
