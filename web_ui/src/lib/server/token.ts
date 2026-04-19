import { readFile } from 'node:fs/promises';

let cached: string | null | undefined;

export async function loadToken(): Promise<string | null> {
  if (cached !== undefined) return cached;

  const envToken = process.env.HEIMDALLM_API_TOKEN;
  if (envToken && envToken.trim().length > 0) {
    cached = envToken.trim();
    return cached;
  }

  const path = process.env.HEIMDALLM_API_TOKEN_FILE ?? '/data/api_token';
  try {
    const contents = await readFile(path, 'utf-8');
    const trimmed = contents.trim();
    cached = trimmed.length > 0 ? trimmed : null;
  } catch {
    cached = null;
  }
  return cached;
}
