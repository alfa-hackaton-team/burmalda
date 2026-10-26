export const demoCompanies = [
  {
    id: 'U_80',
    slug: 'machinery',
    name: 'Компания U_80',
    detail: 'Обработка металлов · Екатеринбург',
  },
  { id: 'U_76', slug: 'logistics', name: 'Компания U_76', detail: 'Перевозки · Новосибирск' },
  { id: 'U_67', slug: 'technology', name: 'Компания U_67', detail: 'Торговля ИТ · Новосибирск' },
] as const;

export const demoPositions = [
  { id: 'cfo', name: 'Финансовый директор' },
  { id: 'ceo', name: 'Генеральный директор' },
  { id: 'operations', name: 'Операционный директор' },
] as const;

export const demoAccounts = [
  {
    login: 'demo.metal',
    password: 'Metal2026!',
    companyId: 'U_80',
    name: 'Тестовый пользователь U_80',
  },
  {
    login: 'demo.logistics',
    password: 'Logistics2026!',
    companyId: 'U_76',
    name: 'Тестовый пользователь U_76',
  },
  {
    login: 'demo.tech',
    password: 'Tech2026!',
    companyId: 'U_67',
    name: 'Тестовый пользователь U_67',
  },
] as const;

export type DemoSession = { login: string; companyId: string; positionId: string };
const sessionKey = 'burmalda.demo-auth.v1';

export function demoContextId(session: DemoSession): string {
  const company = demoCompanies.find((item) => item.id === session.companyId);
  return `demo-${company?.slug ?? 'unknown'}-${session.positionId}`;
}

export function authenticateDemo(
  login: string,
  password: string,
  companyId: string,
  positionId: string,
): DemoSession | undefined {
  const account = demoAccounts.find(
    (item) =>
      item.login === login.trim() && item.password === password && item.companyId === companyId,
  );
  if (!account || !demoPositions.some((item) => item.id === positionId)) return undefined;
  return { login: account.login, companyId, positionId };
}

export function readDemoSession(): DemoSession | undefined {
  try {
    const value = JSON.parse(
      sessionStorage.getItem(sessionKey) ?? 'null',
    ) as Partial<DemoSession> | null;
    if (
      value &&
      demoAccounts.some(
        (item) => item.login === value.login && item.companyId === value.companyId,
      ) &&
      demoPositions.some((item) => item.id === value.positionId)
    ) {
      return { login: value.login!, companyId: value.companyId!, positionId: value.positionId! };
    }
  } catch {
    return undefined;
  }
  return undefined;
}

export function saveDemoSession(session: DemoSession): void {
  try {
    sessionStorage.setItem(sessionKey, JSON.stringify(session));
  } catch {
    return;
  }
}

export function clearDemoSession(): void {
  try {
    sessionStorage.removeItem(sessionKey);
  } catch {
    return;
  }
}
import { ApiError, type Api } from './api';

export function scopedDemoApi(api: Api, session: DemoSession): Api {
  const contextId = demoContextId(session);
  const account = demoAccounts.find((item) => item.login === session.login)!;
  const forbidden = () =>
    new ApiError(
      { code: 'FORBIDDEN_CONTEXT', message: 'Этот демоконтекст недоступен.', retryable: false },
      403,
    );
  const checkContext = (id: string) => {
    if (id !== contextId) throw forbidden();
  };
  const checkInsight = (id: string) => {
    if (!id.startsWith(`${contextId}--`)) throw forbidden();
  };
  return {
    ...api,
    me: async () => ({ user_id: `demo-${session.login}`, display_name: account.name }),
    contexts: async (signal) =>
      (await api.contexts(signal)).filter((item) => item.context.context_id === contextId),
    feed: (id, topic, cursor, signal) => {
      checkContext(id);
      return api.feed(id, topic, cursor, signal);
    },
    analyse: (id, eventId, key, signal) => {
      checkContext(id);
      return api.analyse(id, eventId, key, signal);
    },
    insight: (id, signal) => {
      checkInsight(id);
      return api.insight(id, signal);
    },
    conversations: (id, signal) => {
      checkInsight(id);
      return api.conversations(id, signal);
    },
    createConversation: (id, signal) => {
      checkInsight(id);
      return api.createConversation(id, signal);
    },
    feedback: (id, input, signal) => {
      checkInsight(id);
      return api.feedback(id, input, signal);
    },
  };
}
