import { describe, expect, it } from 'vitest';
import { ALL_COMMAND_NAMES, COMMANDS, isKnownCommand, resolveCommandName } from './commands';

describe('commands utils', () => {
  it('exports canonical command names and aliases in a flat list', () => {
    expect(ALL_COMMAND_NAMES).toEqual(['/help', '/workspace', '/ws', '/job', '/new', '/status', '/info']);
    expect(ALL_COMMAND_NAMES).toHaveLength(
      COMMANDS.reduce((count, command) => count + 1 + (command.aliases?.length ?? 0), 0),
    );
  });

  it('detects known slash commands with trimming, aliases and case-insensitive heads', () => {
    expect(isKnownCommand('/help')).toBe(true);
    expect(isKnownCommand('  /WS list  ')).toBe(true);
    expect(isKnownCommand('/info')).toBe(true);
    expect(isKnownCommand('hello /help')).toBe(false);
    expect(isKnownCommand('/unknown')).toBe(false);
  });

  it('resolves aliases and canonical names to canonical command names', () => {
    expect(resolveCommandName('/workspace')).toBe('/workspace');
    expect(resolveCommandName(' /ws ')).toBe('/workspace');
    expect(resolveCommandName('/INFO')).toBe('/status');
    expect(resolveCommandName('/workspace list')).toBe('');
    expect(resolveCommandName('')).toBe('');
  });
});
