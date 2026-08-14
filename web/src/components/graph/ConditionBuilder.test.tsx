import { useState } from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { describe, expect, it } from 'vitest';
import type { TFunction } from 'i18next';
import i18n from '../../i18n';
import { ConditionBuilder } from './ConditionBuilder';

const t = i18n.t.bind(i18n) as TFunction;

const ADVANCED_ARIA = 'Advanced condition expression';
const LEFT_VAR_ARIA = 'Left variable';

// Stateful host mirroring GraphInspector: the builder's onChange output is fed
// straight back in as the `value` prop (the node config is the source of truth).
function Harness({ initialValue }: { initialValue: string }) {
  const [value, setValue] = useState(initialValue);
  return (
    <ConditionBuilder
      value={value}
      availableVars={['foo', 'bar']}
      fieldId="n1"
      onChange={setValue}
      t={t}
    />
  );
}

describe('ConditionBuilder mode stability', () => {
  it('stays in builder mode when a rule left variable is cleared (unparseable echo)', () => {
    render(<Harness initialValue='{{foo}} == "x"' />);
    const leftInput = screen.getByLabelText(LEFT_VAR_ARIA);

    fireEvent.change(leftInput, { target: { value: '' } });

    // The serialized echo '{{}} == "x"' is not parseable; the builder must not
    // bounce the user into advanced mode mid-edit.
    expect(screen.getByLabelText(LEFT_VAR_ARIA)).toHaveValue('');
    expect(screen.queryByLabelText(ADVANCED_ARIA)).not.toBeInTheDocument();
  });

  it('stays in advanced mode when the text passes through a parseable state', () => {
    render(<Harness initialValue='非 {{foo}} == "x"' />);
    const textarea = screen.getByLabelText(ADVANCED_ARIA);

    // Deleting the leading NOT makes the text momentarily parseable; the mode
    // must not flip back to the builder under the user's cursor.
    fireEvent.change(textarea, { target: { value: '{{foo}} == "x"' } });
    expect(screen.getByLabelText(ADVANCED_ARIA)).toHaveValue('{{foo}} == "x"');
    expect(screen.queryByLabelText(LEFT_VAR_ARIA)).not.toBeInTheDocument();

    // Clearing the field to rewrite it must not flip either.
    fireEvent.change(screen.getByLabelText(ADVANCED_ARIA), { target: { value: '' } });
    expect(screen.getByLabelText(ADVANCED_ARIA)).toHaveValue('');
    expect(screen.queryByLabelText(LEFT_VAR_ARIA)).not.toBeInTheDocument();
  });

  it('re-syncs when the value changes externally (e.g. undo/redo)', () => {
    const { rerender } = render(
      <ConditionBuilder
        value='非 {{foo}} == "x"'
        availableVars={['foo', 'bar']}
        fieldId="n1"
        onChange={() => {}}
        t={t}
      />,
    );
    expect(screen.getByLabelText(ADVANCED_ARIA)).toBeInTheDocument();

    rerender(
      <ConditionBuilder
        value='{{bar}} > "2"'
        availableVars={['foo', 'bar']}
        fieldId="n1"
        onChange={() => {}}
        t={t}
      />,
    );
    expect(screen.queryByLabelText(ADVANCED_ARIA)).not.toBeInTheDocument();
    expect(screen.getByLabelText(LEFT_VAR_ARIA)).toHaveValue('bar');
  });
});
