import { loadToken } from '$lib/server/token.js';
import { error, type RequestHandler } from '@sveltejs/kit';

const DAEMON_URL = (process.env.HEIMDALLM_API_URL ?? 'http://127.0.0.1:7842').replace(/\/+$/, '');

export const GET: RequestHandler = async ({ request }) => {
  const token = await loadToken();
  if (!token) {
    error(503, {
      message: 'daemon token missing: set HEIMDALLM_API_TOKEN or mount /data/api_token'
    });
  }

  const upstream = await fetch(`${DAEMON_URL}/events`, {
    headers: {
      Accept: 'text/event-stream',
      'X-Heimdallm-Token': token
    },
    signal: request.signal
  });

  if (!upstream.ok || !upstream.body) {
    error(upstream.status ?? 502, { message: `daemon /events failed: ${upstream.status}` });
  }

  return new Response(upstream.body, {
    status: 200,
    headers: {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive'
    }
  });
};
