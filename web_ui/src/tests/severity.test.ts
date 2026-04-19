import { describe, expect, it } from 'vitest';
import { severityClass, severityOrder } from '../lib/severity.js';

describe('severityClass', () => {
  it('returns red classes for critical', () => {
    expect(severityClass('critical')).toBe('bg-red-100 text-red-700');
  });
  it('returns orange classes for high', () => {
    expect(severityClass('high')).toBe('bg-orange-100 text-orange-700');
  });
  it('returns amber classes for medium', () => {
    expect(severityClass('medium')).toBe('bg-amber-100 text-amber-700');
  });
  it('returns gray classes for low and for unknown', () => {
    expect(severityClass('low')).toBe('bg-gray-100 text-gray-600');
    expect(severityClass('whatever')).toBe('bg-gray-100 text-gray-600');
    expect(severityClass('')).toBe('bg-gray-100 text-gray-600');
  });
  it('is case-insensitive', () => {
    expect(severityClass('HIGH')).toBe('bg-orange-100 text-orange-700');
    expect(severityClass('Critical')).toBe('bg-red-100 text-red-700');
  });
});

describe('severityOrder', () => {
  it('ranks critical highest, unknown lowest', () => {
    const sorted = ['low', 'critical', 'medium', 'unknown', 'high'].sort(
      (a, b) => severityOrder(b) - severityOrder(a)
    );
    expect(sorted).toEqual(['critical', 'high', 'medium', 'low', 'unknown']);
  });
});
