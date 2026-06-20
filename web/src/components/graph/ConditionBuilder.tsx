import { useMemo, useState } from 'react';
import type { TFunction } from 'i18next';
import {
  COND_OPS,
  emptySimpleCondition,
  isSimpleConditionComplete,
  serializeCondition,
  tryParseSimple,
  type CondJoin,
  type CondOp,
  type CondRule,
  type SimpleCondition,
} from './conditionExpr';

interface ConditionBuilderProps {
  // The canonical condition string (source of truth, stored in node config).
  value: string;
  // Variable names selectable for operands (upstream outputs + globals + reserved).
  availableVars: string[];
  readOnly?: boolean;
  // Stable id of the field so React remounts the builder when the selected node
  // changes (keyed by the caller); also used to scope the <datalist> id.
  fieldId: string;
  onChange: (next: string) => void;
  t: TFunction;
}

// Operator labels are intentionally locale-independent: the bare comparison
// symbol, and the literal backend keyword for the word operators. Same display
// in every language, and the symbol matches the token emitted into the preview
// / stored expression.
const OP_LABEL: Record<CondOp, string> = {
  '==': '==',
  '!=': '!=',
  '>': '>',
  '>=': '>=',
  '<': '<',
  '<=': '<=',
  StartWith: 'StartWith',
  EndWith: 'EndWith',
};

const ORDERED_OPS = new Set<CondOp>(['>', '>=', '<', '<=']);

export function ConditionBuilder({
  value,
  availableVars,
  readOnly,
  fieldId,
  onChange,
  t,
}: ConditionBuilderProps) {
  // Decide the initial editing mode ONCE from the incoming string. The component
  // is keyed by node id upstream, so it remounts (and re-runs these initializers)
  // whenever the user selects a different node — no render-time sync needed.
  const initialParsed = useMemo(() => tryParseSimple(value), [value]);
  const [advanced, setAdvanced] = useState<boolean>(value.trim() !== '' && initialParsed === null);
  const [simple, setSimple] = useState<SimpleCondition>(initialParsed ?? emptySimpleCondition());
  const [advancedText, setAdvancedText] = useState<string>(value);

  const listId = `cond-vars-${fieldId}`;

  const commitSimple = (next: SimpleCondition) => {
    setSimple(next);
    onChange(serializeCondition(next));
  };

  const updateRule = (index: number, patch: Partial<CondRule>) => {
    const rules = simple.rules.map((r, i) => (i === index ? { ...r, ...patch } : r));
    commitSimple({ ...simple, rules });
  };
  const addRule = () => {
    commitSimple({
      ...simple,
      rules: [...simple.rules, { leftVar: '', op: '==', rightIsVar: false, rightValue: '' }],
    });
  };
  const removeRule = (index: number) => {
    const rules = simple.rules.filter((_, i) => i !== index);
    commitSimple({ ...simple, rules: rules.length ? rules : emptySimpleCondition().rules });
  };

  const commitAdvanced = (text: string) => {
    setAdvancedText(text);
    onChange(text);
  };

  // Advanced → simple is only offered when the raw text round-trips cleanly,
  // otherwise switching would rewrite or drop the user's expression.
  const advancedParsable = useMemo(() => tryParseSimple(advancedText), [advancedText]);
  const switchToSimple = () => {
    const parsed = tryParseSimple(advancedText);
    if (!parsed) return;
    setSimple(parsed);
    setAdvanced(false);
    onChange(serializeCondition(parsed));
  };
  const switchToAdvanced = () => {
    const text = serializeCondition(simple);
    setAdvancedText(text);
    setAdvanced(true);
    onChange(text);
  };

  const preview = serializeCondition(simple);
  const complete = isSimpleConditionComplete(simple);
  const usesOrdered = simple.rules.some((r) => ORDERED_OPS.has(r.op));

  const VarDatalist = (
    <datalist id={listId}>
      {availableVars.map((v) => (
        <option key={v} value={v} />
      ))}
    </datalist>
  );

  if (advanced) {
    return (
      <div className="gi-cond">
        <div className="gi-cond-modebar">
          <span className="gi-cond-modelabel">{t('graph.inspector.condAdvancedMode')}</span>
          <button
            type="button"
            className="gi-cond-modeswitch"
            disabled={readOnly || advancedParsable === null}
            title={advancedParsable === null ? t('graph.inspector.condCannotSimplify') : undefined}
            onClick={switchToSimple}
          >
            {t('graph.inspector.condUseBuilder')}
          </button>
        </div>
        <textarea
          className="gi-cond-advanced"
          value={advancedText}
          placeholder={'{{verdict}} == "PASS" 且 {{score}} >= "60"'}
          disabled={readOnly}
          onChange={(e) => commitAdvanced(e.target.value)}
        />
        <div className="gi-desc">{t('graph.inspector.condAdvancedHint')}</div>
      </div>
    );
  }

  return (
    <div className="gi-cond">
      <div className="gi-cond-modebar">
        {simple.rules.length > 1 ? (
          <div className="gi-seg gi-cond-join">
            <button
              type="button"
              className={simple.join === '且' ? 'active' : ''}
              disabled={readOnly}
              onClick={() => commitSimple({ ...simple, join: '且' as CondJoin })}
            >
              {t('graph.inspector.condMatchAll')}
            </button>
            <button
              type="button"
              className={simple.join === '或' ? 'active' : ''}
              disabled={readOnly}
              onClick={() => commitSimple({ ...simple, join: '或' as CondJoin })}
            >
              {t('graph.inspector.condMatchAny')}
            </button>
          </div>
        ) : (
          <span className="gi-cond-modelabel">{t('graph.inspector.condBuilderMode')}</span>
        )}
        <button
          type="button"
          className="gi-cond-modeswitch"
          disabled={readOnly}
          onClick={switchToAdvanced}
        >
          {t('graph.inspector.condUseAdvanced')}
        </button>
      </div>

      {simple.rules.map((rule, i) => (
        <div className="gi-cond-rule" key={i}>
          <div className="gi-cond-rule-top">
            <input
              className="gi-cond-var"
              list={listId}
              value={rule.leftVar}
              placeholder={t('graph.inspector.condLeftPlaceholder')}
              disabled={readOnly}
              onChange={(e) => updateRule(i, { leftVar: e.target.value })}
            />
            <button
              type="button"
              className="gi-cond-del"
              disabled={readOnly || simple.rules.length <= 1}
              aria-label={t('graph.inspector.condRemoveRule')}
              onClick={() => removeRule(i)}
            >
              ×
            </button>
          </div>
          <select
            className="gi-cond-op"
            value={rule.op}
            disabled={readOnly}
            onChange={(e) => updateRule(i, { op: e.target.value as CondOp })}
          >
            {COND_OPS.map((op) => (
              <option key={op} value={op}>
                {OP_LABEL[op]}
              </option>
            ))}
          </select>
          <input
            className="gi-cond-val"
            list={rule.rightIsVar ? listId : undefined}
            value={rule.rightValue}
            placeholder={
              rule.rightIsVar
                ? t('graph.inspector.condLeftPlaceholder')
                : t('graph.inspector.condValuePlaceholder')
            }
            disabled={readOnly}
            onChange={(e) => updateRule(i, { rightValue: e.target.value })}
          />
          <label className="gi-cond-rightvar">
            <input
              type="checkbox"
              checked={rule.rightIsVar}
              disabled={readOnly}
              onChange={(e) => updateRule(i, { rightIsVar: e.target.checked })}
            />
            {t('graph.inspector.condRightIsVar')}
          </label>
        </div>
      ))}

      {!readOnly && (
        <button type="button" className="gi-add-btn gi-cond-add" onClick={addRule}>
          {t('graph.inspector.condAddRule')}
        </button>
      )}

      <div className="gi-cond-opts">
        <label>
          <input
            type="checkbox"
            checked={simple.ignoreCase}
            disabled={readOnly}
            onChange={(e) => commitSimple({ ...simple, ignoreCase: e.target.checked })}
          />
          {t('graph.inspector.condIgnoreCase')}
        </label>
        <label>
          <input
            type="checkbox"
            checked={simple.ignoreSpace}
            disabled={readOnly}
            onChange={(e) => commitSimple({ ...simple, ignoreSpace: e.target.checked })}
          />
          {t('graph.inspector.condIgnoreSpace')}
        </label>
      </div>

      {usesOrdered && <div className="gi-cond-note">{t('graph.inspector.condOrderedHint')}</div>}

      <div className="gi-cond-preview">
        <span className="gi-cond-preview-label">{t('graph.inspector.condPreview')}</span>
        <code>{complete ? preview : t('graph.inspector.condPreviewIncomplete')}</code>
      </div>

      {VarDatalist}
    </div>
  );
}
