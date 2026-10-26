import type {
  AgentResponse,
  ApiErrorData,
  AvailableContext,
  Conversation,
  Feedback,
  FeedbackInput,
  FeedItem,
  FollowUp,
  Generation,
  Insight,
  LoginResult,
  Message,
  NewsItem,
  Page,
  RegistrationCompany,
  RegistrationInput,
  Trace,
  User,
} from './types';

export class ApiError extends Error {
  constructor(
    public readonly data: ApiErrorData,
    public readonly status = 0,
  ) {
    super(data.message);
  }
}

export interface Api {
  login(login: string, password: string, signal?: AbortSignal): Promise<LoginResult>;
  register(input: RegistrationInput, signal?: AbortSignal): Promise<{ user_id: string }>;
  registrationCompany(uid: string, signal?: AbortSignal): Promise<RegistrationCompany>;
  logout(signal?: AbortSignal): Promise<{ signed_out: boolean }>;
  me(signal?: AbortSignal): Promise<User>;
  contexts(signal?: AbortSignal): Promise<AvailableContext[]>;
  news(signal?: AbortSignal): Promise<NewsItem[]>;
  feed(
    contextId: string,
    topic?: string,
    cursor?: string,
    signal?: AbortSignal,
  ): Promise<Page<FeedItem>>;
  analyse(
    contextId: string,
    eventId: string,
    key: string,
    signal?: AbortSignal,
    forceNew?: boolean,
  ): Promise<Insight | Generation>;
  generation(id: string, signal?: AbortSignal): Promise<Generation>;
  insight(id: string, signal?: AbortSignal): Promise<Insight>;
  conversations(insightId: string, signal?: AbortSignal): Promise<Page<Conversation>>;
  createConversation(insightId: string, signal?: AbortSignal): Promise<Conversation>;
  messages(id: string, cursor?: string, signal?: AbortSignal): Promise<Page<Message>>;
  ask(id: string, text: string, key: string, signal?: AbortSignal): Promise<FollowUp | Generation>;
  feedback(id: string, input: FeedbackInput, signal?: AbortSignal): Promise<Feedback>;
  trace(id: string, signal?: AbortSignal): Promise<Trace>;
}

export function csrfToken(): string {
  const cookie = document.cookie.split('; ').find((item) => item.startsWith('csrf_token='));
  return cookie ? decodeURIComponent(cookie.slice('csrf_token='.length)) : '';
}

export function createHttpApi(baseUrl = ''): Api {
  async function request<T>(
    path: string,
    options: RequestInit = {},
    requireCsrf = true,
  ): Promise<T> {
    const headers = new Headers(options.headers);
    headers.set('Accept', 'application/json');
    if (options.body) headers.set('Content-Type', 'application/json');
    if (requireCsrf && options.method && options.method !== 'GET') {
      const token = csrfToken();
      if (!token)
        throw new ApiError({
          code: 'UNAUTHENTICATED',
          message: 'Для действия нужна действующая сессия. Войдите в сервис заново.',
          retryable: false,
        });
      headers.set('X-CSRF-Token', token);
    }
    let response: Response;
    try {
      response = await fetch(`${baseUrl.replace(/\/$/, '')}/v0.1${path}`, {
        ...options,
        headers,
        credentials: 'include',
        cache: 'no-store',
      });
    } catch (error) {
      if (error instanceof Error && error.name === 'AbortError') throw error;
      throw new ApiError({
        code: 'NETWORK_ERROR',
        message: 'Не удалось связаться с сервером. Проверьте подключение и повторите запрос.',
        retryable: true,
      });
    }
    const body: unknown = await response.json().catch(() => null);
    if (!response.ok) {
      if (response.status === 401 && !path.startsWith('/auth/')) {
        window.dispatchEvent(new Event('burmalda:session-expired'));
      }
      const data = body as Partial<ApiErrorData> | null;
      throw new ApiError(
        {
          code: data?.code ?? 'HTTP_ERROR',
          message: data?.message ?? `Сервер вернул ошибку ${response.status}.`,
          retryable: data?.retryable ?? response.status >= 500,
          trace_id: data?.trace_id,
        },
        response.status,
      );
    }
    if (!body)
      throw new ApiError({
        code: 'INVALID_RESPONSE',
        message: 'Сервер вернул пустой ответ.',
        retryable: false,
      });
    return body as T;
  }
  const post = <T>(path: string, body: unknown, key?: string, signal?: AbortSignal) =>
    request<T>(path, {
      method: 'POST',
      body: JSON.stringify(body),
      signal,
      headers: key ? { 'Idempotency-Key': key } : {},
    });
  const enc = encodeURIComponent;
  return {
    login: (login, password, signal) =>
      request(
        '/auth/login',
        { method: 'POST', body: JSON.stringify({ login, password }), signal },
        false,
      ),
    register: (input, signal) =>
      request('/auth/register', { method: 'POST', body: JSON.stringify(input), signal }, false),
    registrationCompany: (uid, signal) =>
      request(`/auth/registration-company?uid=${enc(uid)}`, { signal }),
    logout: (signal) => post('/auth/logout', {}, undefined, signal),
    me: (signal) => request('/me', { signal }),
    async contexts(signal) {
      const items: AvailableContext[] = [];
      let cursor: string | null = null;
      do {
        const page: Page<AvailableContext> = await request(
          `/contexts?limit=100${cursor ? `&cursor=${enc(cursor)}` : ''}`,
          { signal },
        );
        items.push(...page.items);
        cursor = page.next_cursor;
      } while (cursor);
      return items;
    },
    news: async (signal) => (await request<{ items: NewsItem[] }>('/news', { signal })).items,
    feed: (id, topic, cursor, signal) =>
      request(
        `/feed?${new URLSearchParams({ context_id: id, limit: '20', ...(topic ? { topic } : {}), ...(cursor ? { cursor } : {}) })}`,
        { signal },
      ),
    analyse: (context_id, event_id, key, signal, forceNew) =>
      post(
        '/insights',
        { context_id, event_id, ...(forceNew ? { force_new: true } : {}) },
        key,
        signal,
      ),
    generation: (id, signal) => request(`/generations/${enc(id)}`, { signal }),
    insight: (id, signal) => request(`/insights/${enc(id)}`, { signal }),
    conversations: (id, signal) => request(`/insights/${enc(id)}/conversations`, { signal }),
    createConversation: (id, signal) =>
      post(`/insights/${enc(id)}/conversations`, {}, undefined, signal),
    messages: (id, cursor, signal) =>
      request(
        `/conversations/${enc(id)}/messages?limit=100${cursor ? `&cursor=${enc(cursor)}` : ''}`,
        { signal },
      ),
    ask: (id, text, key, signal) =>
      post(`/conversations/${enc(id)}/messages`, { text }, key, signal),
    feedback: (id, input, signal) =>
      post(`/insights/${enc(id)}/feedback`, input, undefined, signal),
    trace: (id, signal) => request(`/traces/${enc(id)}`, { signal }),
  };
}

export function abortableDelay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('Aborted', 'AbortError'));
      return;
    }
    const abort = () => {
      clearTimeout(timer);
      reject(new DOMException('Aborted', 'AbortError'));
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', abort);
      resolve();
    }, ms);
    signal?.addEventListener('abort', abort, { once: true });
  });
}

export async function waitForGeneration(
  api: Api,
  initial: Generation,
  signal: AbortSignal,
  update?: (state: Generation['state']) => void,
): Promise<Generation> {
  let result = initial;
  const deadline = Date.now() + 90_000;
  while (result.state === 'QUEUED' || result.state === 'RUNNING') {
    update?.(result.state);
    if (Date.now() > deadline)
      throw new ApiError({
        code: 'TIMEOUT',
        message: 'Анализ еще выполняется. Повторная проверка использует прежнюю операцию.',
        retryable: true,
      });
    await abortableDelay(1200, signal);
    result = await api.generation(result.generation_id, signal);
  }
  if (result.state === 'FAILED')
    throw new ApiError({
      ...(result.error ?? {
        code: 'ML_PROCESSING_ERROR',
        message: 'Не удалось подготовить анализ.',
        retryable: false,
      }),
      trace_id: result.trace_id,
    });
  return result;
}

export async function loadMessages(
  api: Api,
  conversationId: string,
  signal: AbortSignal,
): Promise<Message[]> {
  const messages: Message[] = [];
  let cursor: string | null = null;
  do {
    const page = await api.messages(conversationId, cursor ?? undefined, signal);
    messages.push(...page.items);
    cursor = page.next_cursor;
  } while (cursor);
  return messages.sort((a, b) => a.sequence - b.sequence);
}

export function visibleClaims(response: AgentResponse) {
  return response.claims.filter((claim) => claim.verification_status !== 'UNSUPPORTED');
}

export function safeExternalUrl(value?: string): string | undefined {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password
      ? url.href
      : undefined;
  } catch {
    return undefined;
  }
}
