import { abortableDelay, ApiError, type Api } from './api';
import { demoCompanies, demoPositions } from './demo-auth';
import type {
  AgentResponse,
  AvailableContext,
  Conversation,
  Feedback,
  FeedItem,
  Insight,
  Message,
  Page,
  SectionType,
  Trace,
} from './types';

const stamp = '2026-10-25T09:00:00Z';
export const demoContexts: AvailableContext[] = demoCompanies.flatMap((company) =>
  demoPositions.map((position) => ({
    context: {
      context_id: `demo-${company.slug}-${position.id}`,
      kind: 'COMPANY' as const,
      company_id: company.id,
    },
    display_name: `${company.name} · ${position.name}`,
    role_id: `demo-${position.id}`,
    profile: { profile_id: `demo-profile-${company.slug}`, version: '1' },
  })),
);

const stories = [
  {
    event_id: 'demo-financing',
    title: 'Изменение кредитных условий: что проверить перед новым договором',
    summary:
      'Учебный сценарий: банк-поставщик финансирования объявил новые условия для будущих договоров. Действующие договоры требуют отдельной проверки.',
    topic: 'financing',
    label: 'Требует внимания',
  },
  {
    event_id: 'demo-logistics',
    title: 'Новые сроки перевозок на одном из маршрутов поставок',
    summary:
      'Учебный сценарий: перевозчик сообщил об изменении сроков доставки. Влияние зависит от маршрутов и запаса комплектующих компании.',
    topic: 'logistics',
    label: 'Потенциальный риск',
  },
  {
    event_id: 'demo-deposits',
    title: 'Размещение свободных средств: обновились условия депозитов',
    summary:
      'Учебный сценарий: появились новые условия размещения средств. Применимость зависит от платежного календаря и доступной ликвидности.',
    topic: 'liquidity',
    label: 'Возможность',
  },
  {
    event_id: 'demo-regulation',
    title: 'Изменения в отчетности: проверьте применимость к компании',
    summary:
      'Учебный сценарий: опубликовано разъяснение о подготовке отчетности. Для вывода о применимости нужны дополнительные сведения.',
    topic: 'regulation',
    label: 'Нужны данные',
  },
];

function contextFor(id: string): AvailableContext {
  const context = demoContexts.find((item) => item.context.context_id === id);
  if (!context)
    throw new ApiError({
      code: 'FORBIDDEN_CONTEXT',
      message: 'Этот контекст недоступен.',
      retryable: false,
    });
  return context;
}

export function demoFeed(contextId: string): FeedItem[] {
  const context = contextFor(contextId);
  const logistics = contextId.includes('logistics');
  const ordered = logistics ? [stories[1], stories[3], stories[0], stories[2]] : stories;
  return ordered.map((story, index) => ({
    preview_id: `${contextId}-${story.event_id}`,
    event: { event_id: story.event_id, title: story.title, summary: story.summary },
    published_at: `2026-10-25T0${9 - index}:00:00Z`,
    topic: story.topic,
    why_relevant:
      story.topic === 'financing'
        ? context.role_id === 'demo-ceo'
          ? 'Важно для планирования инвестиций: в учебном профиле есть кредитный продукт.'
          : 'В учебном профиле есть кредитный продукт. Условия новых заимствований требуют проверки.'
        : story.topic === 'logistics'
          ? 'В учебном профиле отмечены поставки. Конкретные маршруты и сроки требуют уточнения.'
          : story.topic === 'liquidity'
            ? 'В профиле есть депозитный продукт. Сумма свободных средств неизвестна.'
            : 'Применимость к компании пока не установлена: нужны дополнительные данные.',
    feature_refs: [
      {
        profile: context.profile!,
        feature_id: `demo-${story.topic}`,
        contributing_company_ids: [
          context.context.kind === 'COMPANY' ? context.context.company_id : 'demo-company',
        ],
      },
    ],
    sources: [
      { source_id: 'demo-news', name: 'Учебная новостная публикация', source_role: 'external' },
      ...(story.topic !== 'regulation'
        ? [
            {
              source_id: 'demo-bank',
              name: 'Учебный аналитический материал',
              source_role: 'bank' as const,
            },
          ]
        : []),
    ],
    impact: {
      label: story.label,
      explanation: 'Демонстрационная оценка для проверки интерфейса, не результат модели.',
      policy_version: 'demo-v1',
    },
  }));
}

export function demoInsight(contextId: string, eventId: string): Insight {
  const context = contextFor(contextId);
  const item = demoFeed(contextId).find((entry) => entry.event.event_id === eventId)!;
  if (!item)
    throw new ApiError({ code: 'NOT_FOUND', message: 'Событие не найдено.', retryable: false });
  const ceo = context.role_id === 'demo-ceo';
  const financing = eventId === 'demo-financing';
  const regulation = eventId === 'demo-regulation';
  const profileFeature = financing
    ? 'Наличие кредитного продукта: да. Тип ставки и график платежей неизвестны.'
    : eventId === 'demo-deposits'
      ? 'Наличие депозитного продукта: да. Доступный остаток и платежный календарь неизвестны.'
      : 'В учебном профиле есть деятельность, связанная с поставками. Маршруты и договорные условия неизвестны.';
  const entries: {
    id: string;
    type: SectionType;
    text: string;
    kind: 'FACT' | 'ALFA_ANALYTICS' | 'AI_INFERENCE';
    partial?: boolean;
  }[] = [
    { id: 'fact', type: 'WHAT_HAPPENED', text: item.event.summary, kind: 'FACT' },
    { id: 'relevance', type: 'WHY_RELEVANT', text: item.why_relevant, kind: 'AI_INFERENCE' },
    ...(regulation
      ? []
      : [
          {
            id: 'bank',
            type: 'BANK_VIEW' as const,
            text: 'Учебный пример, не позиция Альфа-Банка: оценивать изменения следует вместе с условиями договоров и горизонтом планирования. Здесь показано место для цитаты из реального отчета банка.',
            kind: 'ALFA_ANALYTICS' as const,
          },
        ]),
    {
      id: 'analysis',
      type: 'AI_ANALYSIS',
      text: financing
        ? ceo
          ? 'При планировании инвестиционной программы сравните сценарии доступности нового финансирования. Наличие кредита само по себе не доказывает изменение действующих платежей.'
          : 'Если компания планирует новые заимствования, обновленные условия могут изменить будущую стоимость финансирования. Автоматически переносить это на действующий кредит нельзя.'
        : eventId === 'demo-deposits'
          ? 'Размещение средств можно рассмотреть только после проверки ближайших обязательств. Наличие депозитного продукта не подтверждает свободную ликвидность.'
          : 'Потенциальное влияние зависит от фактических договоров и операций компании. По имеющемуся профилю нельзя вычислить сумму эффекта.',
      kind: 'AI_INFERENCE',
      partial: true,
    },
    {
      id: 'risk',
      type: 'RISKS',
      text: financing
        ? 'Бюджет будущего заимствования может отличаться от планового. Величина изменения неизвестна без предложения банка и параметров договора.'
        : 'Решение без уточнения исходных данных может привести к неверной оценке сроков и потребности в средствах.',
      kind: 'AI_INFERENCE',
      partial: true,
    },
    {
      id: 'action',
      type: 'SCENARIOS_ACTIONS',
      text: financing
        ? 'Уточнить планы заимствований, запросить актуальные условия и проверить тип ставки действующих договоров.'
        : 'Проверить применимость события к действующим договорам и актуализировать план платежей; затем пересчитать сценарий.',
      kind: 'AI_INFERENCE',
    },
  ];
  const claims = entries.map((entry) => ({
    claim_id: entry.id,
    text: entry.text,
    claim_type: entry.kind,
    verification_status: entry.partial ? ('PARTIALLY_SUPPORTED' as const) : ('SUPPORTED' as const),
    evidence_links: [
      {
        evidence_id: entry.kind === 'ALFA_ANALYTICS' ? 'demo-evidence-bank' : 'demo-evidence-news',
        relation: 'SUPPORT' as const,
      },
      ...(entry.kind === 'AI_INFERENCE'
        ? [{ evidence_id: 'demo-evidence-company', relation: 'CONTEXT' as const }]
        : []),
    ],
    assumptions: entry.partial
      ? ['Применимость сценария зависит от неизвестных параметров компании.']
      : [],
  }));
  return {
    insight_id: `${contextId}--${eventId}`,
    revision: 'demo-v1',
    primary_event: item.event,
    supporting_event_ids: [],
    context: context.context,
    role_id: context.role_id,
    profile: context.profile!,
    created_at: stamp,
    response: {
      trace_id: `trace-${contextId}--${eventId}`,
      claims,
      evidence: [
        {
          evidence_id: 'demo-evidence-news',
          evidence_type: 'EXTERNAL_NEWS',
          reference: {
            source_id: 'demo-news',
            document_version_id: `demo-doc-${eventId}-v1`,
            chunk_id: 'demo-chunk-1',
          },
          excerpt: item.event.summary,
          published_at: stamp,
          fetched_at: stamp,
        },
        {
          evidence_id: 'demo-evidence-company',
          evidence_type: 'COMPANY_DATA',
          reference: item.feature_refs[0],
          content: profileFeature,
          provenance: {
            source_snapshot_id: 'demo-profile-snapshot',
            record_refs: ['demo-row'],
            derivation_version: 'demo-v1',
          },
        },
        ...(regulation
          ? []
          : [
              {
                evidence_id: 'demo-evidence-bank',
                evidence_type: 'ALFA_ANALYTICS' as const,
                reference: {
                  source_id: 'demo-bank',
                  document_version_id: 'demo-bank-doc-v1',
                  chunk_id: 'demo-bank-chunk',
                },
                excerpt:
                  'Синтетический текст для проверки представления аналитики банка. Не является публикацией или позицией Альфа-Банка.',
                published_at: stamp,
                fetched_at: stamp,
              },
            ]),
      ],
      sections: entries.map((entry) => ({ section_type: entry.type, claim_ids: [entry.id] })),
      limitations: [
        {
          code: 'INCOMPLETE_PROFILE',
          message: financing
            ? 'Тип ставки, сумма кредита и планы новых заимствований неизвестны.'
            : 'Данных для количественной оценки недостаточно.',
          related_claim_ids: ['analysis', 'risk'],
        },
        ...(regulation
          ? [
              {
                code: 'NO_BANK_VIEW',
                message: 'Подходящий материал банка не найден. Его позиция не сформирована.',
              },
            ]
          : []),
      ],
      model_config_ref: 'synthetic-ui-fixture-v1',
    },
  };
}

type DemoState = { conversations: Conversation[]; messages: Message[]; feedback: Feedback[] };
const storageKey = 'burmalda.synthetic-ui.v1';
const emptyState = (): DemoState => ({ conversations: [], messages: [], feedback: [] });
function readState(): DemoState {
  try {
    const value = JSON.parse(sessionStorage.getItem(storageKey) ?? 'null') as DemoState | null;
    return value &&
      Array.isArray(value.conversations) &&
      Array.isArray(value.messages) &&
      Array.isArray(value.feedback)
      ? value
      : emptyState();
  } catch {
    return emptyState();
  }
}

export function createDemoApi(scenario = ''): Api {
  const state = readState();
  const insights = new Map<string, Insight>();
  const requests = new Map<
    string,
    Insight | { user_message: Message; assistant_message: Message }
  >();
  const save = () => {
    try {
      sessionStorage.setItem(storageKey, JSON.stringify(state));
    } catch {
      /* Demo still works without browser storage. */
    }
  };
  const page = <T>(items: T[]): Page<T> => ({ items, next_cursor: null });
  const pause = (signal?: AbortSignal, ms = 350) => abortableDelay(ms, signal);
  return {
    login: async () => {
      throw new Error('Демовход работает без серверной сессии.');
    },
    register: async () => {
      throw new Error('Регистрация доступна только с сервером.');
    },
    registrationCompany: async () => {
      throw new Error('Демокомпании заданы локально.');
    },
    logout: async () => ({ signed_out: true }),
    async me(signal) {
      await pause(signal);
      if (scenario === 'unauthenticated')
        throw new ApiError(
          {
            code: 'UNAUTHENTICATED',
            message: 'Сессия завершилась. Откройте сервис через интернет-банк.',
            retryable: false,
          },
          401,
        );
      return { user_id: 'demo-user', display_name: 'Анна Смирнова' };
    },
    async contexts(signal) {
      await pause(signal);
      return scenario === 'no-contexts' ? [] : demoContexts;
    },
    async feed(id, topic, _cursor, signal) {
      await pause(signal);
      if (scenario === 'error')
        throw new ApiError({
          code: 'SOURCE_UNAVAILABLE',
          message: 'Не удалось получить ленту. Сохраненные разборы не удалены.',
          retryable: true,
        });
      if (scenario === 'profile')
        throw new ApiError({
          code: 'PROFILE_NOT_READY',
          message: 'Профиль компании готовится. Лента появится после завершения обработки.',
          retryable: true,
        });
      return page(
        scenario === 'empty' ? [] : demoFeed(id).filter((item) => !topic || item.topic === topic),
      );
    },
    async analyse(contextId, eventId, key, signal) {
      await pause(signal, scenario === 'slow' ? 7000 : 1000);
      if (requests.has(key)) return requests.get(key) as Insight;
      const value = demoInsight(contextId, eventId);
      insights.set(value.insight_id, value);
      requests.set(key, value);
      return value;
    },
    async generation() {
      throw new ApiError({
        code: 'NOT_FOUND',
        message: 'Демонстрационная операция не найдена.',
        retryable: false,
      });
    },
    async insight(id) {
      const value = insights.get(id);
      if (!value)
        throw new ApiError({ code: 'NOT_FOUND', message: 'Разбор не найден.', retryable: false });
      return value;
    },
    async conversations(id, signal) {
      await pause(signal);
      return page(state.conversations.filter((item) => item.insight_id === id));
    },
    async createConversation(id, signal) {
      await pause(signal);
      const insight = insights.get(id)!;
      const existing = state.conversations.find((item) => item.insight_id === id);
      if (existing) return existing;
      const value: Conversation = {
        conversation_id: crypto.randomUUID(),
        insight_id: id,
        insight_revision: insight.revision,
        context: insight.context,
        role_id: insight.role_id,
        created_at: new Date().toISOString(),
      };
      state.conversations.push(value);
      save();
      return value;
    },
    async messages(id, _cursor, signal) {
      await pause(signal);
      return page(state.messages.filter((item) => item.conversation_id === id));
    },
    async ask(id, text, key, signal) {
      await pause(signal, 850);
      if (requests.has(key))
        return requests.get(key) as { user_message: Message; assistant_message: Message };
      const conversation = state.conversations.find((item) => item.conversation_id === id)!;
      const insight = insights.get(conversation.insight_id)!;
      const sequence = state.messages.filter((item) => item.conversation_id === id).length + 1;
      const user_message: Message = {
        message_id: crypto.randomUUID(),
        conversation_id: id,
        sequence,
        author: 'USER',
        created_at: new Date().toISOString(),
        text,
      };
      const response: AgentResponse = {
        ...insight.response,
        sections: [{ section_type: 'AI_ANALYSIS', claim_ids: ['follow-up'] }],
        claims: [
          {
            claim_id: 'follow-up',
            text: 'Это демонстрационный ответ, без обращения к модели. В рабочем режиме уточнение будет проверяться по источникам текущего разбора. По этому учебному профилю нельзя назвать точную сумму эффекта: сначала нужны параметры договоров и платежный календарь.',
            claim_type: 'AI_INFERENCE',
            verification_status: 'PARTIALLY_SUPPORTED',
            assumptions: ['Ответ относится только к выбранному учебному разбору.'],
            evidence_links: [{ evidence_id: 'demo-evidence-company', relation: 'CONTEXT' }],
          },
        ],
      };
      const assistant_message: Message = {
        message_id: crypto.randomUUID(),
        conversation_id: id,
        sequence: sequence + 1,
        author: 'ASSISTANT',
        created_at: new Date().toISOString(),
        response,
      };
      state.messages.push(user_message, assistant_message);
      save();
      const value = { user_message, assistant_message };
      requests.set(key, value);
      return value;
    },
    async feedback(id, input, signal) {
      await pause(signal);
      const value: Feedback = {
        feedback_id: crypto.randomUUID(),
        insight_id: id,
        created_at: new Date().toISOString(),
        input,
      };
      state.feedback = [
        ...state.feedback.filter(
          (entry) =>
            entry.insight_id !== id || entry.input.insight_revision !== input.insight_revision,
        ),
        value,
      ];
      save();
      return value;
    },
    async trace(id, signal): Promise<Trace> {
      await pause(signal);
      return {
        trace_id: id,
        stages: [
          'Проверен учебный профиль компании',
          'Подобраны демонстрационные материалы',
          'Разделены факты, аналитика и выводы',
          'Отмечены неизвестные данные',
        ].map((summary, index) => ({
          stage_id: `stage-${index}`,
          stage_type: 'DEMO',
          status: 'SUCCEEDED',
          started_at: stamp,
          finished_at: stamp,
          evidence_ids: [],
          feature_refs: [],
          summary,
        })),
      };
    },
  };
}
