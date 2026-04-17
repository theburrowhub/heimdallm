import { loadToken } from '$lib/server/token.js';
import { error, type RequestHandler } from '@sveltejs/kit';

const DAEMON_URL = (process.env.HEIMDALLM_API_URL ?? 'http://127.0.0.1:7842').replace(/\/+$/, '');

const handle: RequestHandler = async ({ params, request, url }) => {
  const token = await loadToken();
  if (!token) {
    error(503, {
      message: 'daemon token missing: set HEIMDALLM_API_TOKEN or mount /data/api_token'
    });
  }

  const target = new URL(`${DAEMON_URL}/${params.path ?? ''}`);
  target.search = url.search;

  const headers = new Headers();
  const contentType = request.headers.get('content-type');
  if (contentType) headers.set('content-type', contentType);
  headers.set('X-Heimdallm-Token', token);

  const init: RequestInit = {
    method: request.method,
    headers,
    signal: request.signal,
    // @ts-expect-error duplex is required by Node fetch when streaming a body
    duplex: 'half'
  };
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    init.body = request.body;
  }

  let upstream: Response;
  try {
    upstream = await fetch(target, init);
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    error(502, { message: `daemon unreachable at ${DAEMON_URL}: ${msg}` });
  }

  const respHeaders = new Headers();
  const upstreamCt = upstream.headers.get('content-type');
  if (upstreamCt) respHeaders.set('content-type', upstreamCt);

  return new Response(upstream.body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers: respHeaders
  });
};

export const GET = handle;
export const POST = handle;
export const PUT = handle;
export const DELETE = handle;
export const PATCH = handle;
