import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, createHttpApi, safeExternalUrl, visibleClaims, waitForGeneration } from './api';
import { createDemoApi, demoFeed, demoInsight } from './demo';
import type { Api } from './api';
import type { Generation } from './types';

beforeEach(() => {
  document.cookie = 'csrf_token=; Max-Age=0; path=/';
  sessionStorage.clear();
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('public API boundaries', () => {
  it('uses the session, CSRF and stable operation key without sending a client-assigned role', async () => {
    document.cookie = 'csrf_token=session-proof; path=/';
    const fetch = vi
      .fn()
      .mockImplementation(
        async () => new Response(JSON.stringify({ insight_id: 'result' }), { status: 200 }),
      );
    vi.stubGlobal('fetch', fetch);
    const api = createHttpApi('https://example.test/');
    await api.analyse('context-a', 'event-a', 'stable-key');
    await api.analyse('context-a', 'event-a', 'stable-key');
    expect(fetch).toHaveBeenCalledTimes(2);
    for (const [url, options] of fetch.mock.calls) {
      expect(url).toBe('https://example.test/v0.1/insights');
      expect(options.credentials).toBe('include');
      expect(options.cache).toBe('no-store');
      expect(options.headers.get('X-CSRF-Token')).toBe('session-proof');
      expect(options.headers.get('Idempotency-Key')).toBe('stable-key');
      expect(JSON.parse(options.body)).toEqual({ context_id: 'context-a', event_id: 'event-a' });
    }
  });

  it('does not send a mutation without CSRF', async () => {
    const fetch = vi.fn();
    vi.stubGlobal('fetch', fetch);
    await expect(createHttpApi().analyse('c', 'e', 'key')).rejects.toMatchObject({
      data: { code: 'UNAUTHENTICATED' },
    });
    expect(fetch).not.toHaveBeenCalled();
  });

  it('starts a server session without CSRF and requires it for logout', async () => {
    const fetch = vi
      .fn()
      .mockImplementation(
        async (url: string) =>
          new Response(
            JSON.stringify(
              url.endsWith('/logout')
                ? { signed_out: true }
                : { user_id: 'u', expires_at: '2026-10-26T00:00:00Z' },
            ),
          ),
      );
    vi.stubGlobal('fetch', fetch);
    const api = createHttpApi();
    await api.login('user', 'secret');
    expect(fetch.mock.calls[0][0]).toBe('/v0.1/auth/login');
    expect(fetch.mock.calls[0][1].credentials).toBe('include');
    expect(fetch.mock.calls[0][1].headers.has('X-CSRF-Token')).toBe(false);
    await expect(api.logout()).rejects.toMatchObject({ data: { code: 'UNAUTHENTICATED' } });
    document.cookie = 'csrf_token=session-proof; path=/';
    await api.logout();
    expect(fetch.mock.calls[1][0]).toBe('/v0.1/auth/logout');
    expect(fetch.mock.calls[1][1].headers.get('X-CSRF-Token')).toBe('session-proof');
  });

  it('keeps authorization errors instead of replacing them with demo results', async () => {
    const expired = vi.fn();
    window.addEventListener('burmalda:session-expired', expired, { once: true });
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          new Response(
            JSON.stringify({ code: 'UNAUTHENTICATED', message: 'Sign in', retryable: false }),
            { status: 401 },
          ),
        ),
    );
    await expect(createHttpApi().me()).rejects.toMatchObject({
      status: 401,
      data: { code: 'UNAUTHENTICATED' },
    });
    expect(expired).toHaveBeenCalledTimes(1);
  });

  it('loads stored news with the session and without browser HTTP caching', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ items: [] })));
    vi.stubGlobal('fetch', fetch);
    expect(await createHttpApi().news()).toEqual([]);
    expect(fetch.mock.calls[0][0]).toBe('/v0.1/news');
    expect(fetch.mock.calls[0][1]).toMatchObject({ credentials: 'include', cache: 'no-store' });
  });

  it('loads all pages of authorized contexts', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ items: [{ context: { context_id: 'a' } }], next_cursor: 'next/token' }),
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ items: [{ context: { context_id: 'b' } }], next_cursor: null }),
        ),
      );
    vi.stubGlobal('fetch', fetch);
    expect(await createHttpApi().contexts()).toHaveLength(2);
    expect(fetch.mock.calls[1][0]).toContain('cursor=next%2Ftoken');
  });

  it('cancels polling without creating another analysis request', async () => {
    const generation = vi.fn();
    const controller = new AbortController();
    const initial: Generation = {
      generation_id: 'g',
      state: 'RUNNING',
      trace_id: 't',
      created_at: '2026-10-25T00:00:00Z',
    };
    const pending = waitForGeneration({ generation } as unknown as Api, initial, controller.signal);
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
    expect(generation).not.toHaveBeenCalled();
  });
});

describe('evidence and demo behavior', () => {
  it('does not render unsupported claims as confirmed content', () => {
    const response = demoInsight('demo-machinery-cfo', 'demo-financing').response;
    response.claims[0].verification_status = 'UNSUPPORTED';
    expect(visibleClaims(response).some((claim) => claim.claim_id === 'fact')).toBe(false);
  });

  it('only permits HTTP(S) source links', () => {
    expect(safeExternalUrl('javascript:alert(1)')).toBeUndefined();
    expect(safeExternalUrl('data:text/html,test')).toBeUndefined();
    expect(safeExternalUrl('https://user:secret@example.test/report')).toBeUndefined();
    expect(safeExternalUrl('https://example.test/report')).toBe('https://example.test/report');
  });

  it('keeps demo claims connected to evidence and separates company contexts', () => {
    const a = demoInsight('demo-machinery-cfo', 'demo-financing');
    const b = demoInsight('demo-machinery-ceo', 'demo-financing');
    expect(a.insight_id).not.toBe(b.insight_id);
    expect(a.response.claims.find((entry) => entry.claim_id === 'analysis')?.text).not.toBe(
      b.response.claims.find((entry) => entry.claim_id === 'analysis')?.text,
    );
    for (const claim of a.response.claims)
      for (const link of claim.evidence_links)
        expect(a.response.evidence.some((entry) => entry.evidence_id === link.evidence_id)).toBe(
          true,
        );
    expect(demoFeed('demo-logistics-cfo')[0].event.event_id).toBe('demo-logistics');
  });

  it('rejects unknown demo contexts', () => {
    expect(() => demoFeed('not-authorized')).toThrow(ApiError);
  });

  it('handles unavailable storage without breaking the demo', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('storage blocked');
    });
    expect(createDemoApi()).toBeDefined();
  });
});
