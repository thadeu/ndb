import { describe, expect, it } from 'bun:test';

import { messageOf } from '@/ui/form-validation';

const valid = { valueMissing: false, patternMismatch: false, typeMismatch: false, tooShort: false, tooLong: false, rangeUnderflow: false, rangeOverflow: false, badInput: false, stepMismatch: false } as ValidityState;

describe('inline form errors', () => {
  it('says what to do for an empty required field', () => {
    expect(messageOf({ ...valid, valueMissing: true }, {})).toBe('Fill in this field.');
  });

  it('uses the text of the field for a pattern', () => {
    expect(messageOf({ ...valid, patternMismatch: true }, { custom: 'Use letters, digits, - and _.' })).toBe('Use letters, digits, - and _.');
    expect(messageOf({ ...valid, patternMismatch: true }, {})).toBe('This value does not have the right form.');
  });

  it('gives the limit for a length or a range', () => {
    expect(messageOf({ ...valid, tooLong: true }, { maxLength: 64 })).toBe('Use 64 characters or less.');
    expect(messageOf({ ...valid, rangeUnderflow: true }, { min: '1' })).toBe('The smallest value is 1.');
  });

  it('falls back to the text of the browser', () => {
    expect(messageOf(valid, { fallback: 'Custom rule.' })).toBe('Custom rule.');
  });
});
