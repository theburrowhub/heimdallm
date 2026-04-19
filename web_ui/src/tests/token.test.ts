/**
 * @vitest-environment node
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

const readFileMock = vi.fn();

vi.mock('node:fs/promises', () => ({
  readFile: (...args: unknown[]) => readFileMock(...args)
}));

const originalEnv = { ...process.env };

beforeEach(async () => {
  vi.resetModules();
  readFileMock.mockReset();
  process.env = { ...originalEnv };
  delete process.env.HEIMDALLM_API_TOKEN;
  delete process.env.HEIMDALLM_API_TOKEN_FILE;
});

describe('loadToken', () => {
  it('returns env var when set and non-empty', async () => {
    process.env.HEIMDALLM_API_TOKEN = 'env-token';
    readFileMock.mockResolvedValue('file-token\n');
    const { loadToken } = await import('../lib/server/token.js');
    expect(await loadToken()).toBe('env-token');
    expect(readFileMock).not.toHaveBeenCalled();
  });

  it('falls back to file when env var missing, trimming trailing newline', async () => {
    readFileMock.mockResolvedValue('file-token\n');
    const { loadToken } = await import('../lib/server/token.js');
    expect(await loadToken()).toBe('file-token');
    expect(readFileMock).toHaveBeenCalledWith('/data/api_token', 'utf-8');
  });

  it('caches the resolved token across calls', async () => {
    readFileMock.mockResolvedValue('file-token\n');
    const { loadToken } = await import('../lib/server/token.js');
    expect(await loadToken()).toBe('file-token');
    expect(await loadToken()).toBe('file-token');
    expect(readFileMock).toHaveBeenCalledTimes(1);
  });

  it('returns null when neither env nor file yields a token', async () => {
    readFileMock.mockRejectedValue(new Error('ENOENT'));
    const { loadToken } = await import('../lib/server/token.js');
    expect(await loadToken()).toBeNull();
  });

  it('uses HEIMDALLM_API_TOKEN_FILE override when set', async () => {
    process.env.HEIMDALLM_API_TOKEN_FILE = '/run/secrets/token';
    readFileMock.mockResolvedValue('secret\n');
    const { loadToken } = await import('../lib/server/token.js');
    expect(await loadToken()).toBe('secret');
    expect(readFileMock).toHaveBeenCalledWith('/run/secrets/token', 'utf-8');
  });
});
