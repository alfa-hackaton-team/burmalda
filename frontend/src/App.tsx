import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Button } from '@alfalab/core-components-button';
import { Input } from '@alfalab/core-components-input';
import { Textarea } from '@alfalab/core-components-textarea';
import { Typography } from '@alfalab/core-components-typography';
import { Spinner } from '@alfalab/core-components-spinner';
import {
  ArrowDownRight,
  ArrowLeft,
  ArrowRight,
  ArrowUpRight,
  Building2,
  Check,
  CheckCheck,
  ChevronDown,
  CircleHelp,
  Clock3,
  FileText,
  Layers3,
  MessageSquare,
  Newspaper,
  RefreshCw,
  Search,
  Send,
  ShieldCheck,
  Sparkles,
  ThumbsDown,
  ThumbsUp,
  X,
} from 'lucide-react';
import {
  ApiError,
  loadMessages,
  safeExternalUrl,
  visibleClaims,
  waitForGeneration,
  type Api,
} from './api';
import type {
  AgentResponse,
  AvailableContext,
  Claim,
  Conversation,
  Evidence,
  FeedbackInput,
  FeedItem,
  Insight,
  Message,
  Page,
  SectionType,
  Trace,
  User,
} from './types';
import { Burmalda } from './Burmalda';

const topics: Record<string, string> = {
  financing: 'Финансирование',
  logistics: 'Логистика',
  liquidity: 'Ликвидность',
  regulation: 'Регулирование',
};
const sectionLabels: Record<SectionType, string> = {
  WHAT_HAPPENED: 'Что произошло',
  WHY_RELEVANT: 'Почему это важно вашей компании',
  RISKS: 'Возможные риски',
  OPPORTUNITIES: 'Возможности',
  SCENARIOS_ACTIONS: 'Что стоит проверить',
  BANK_VIEW: 'Взгляд Альфа-Банка',
  AI_ANALYSIS: 'Влияние на ваш бизнес',
};
const kindLabels: Record<Claim['claim_type'], string> = {
  FACT: 'Факт',
  ALFA_ANALYTICS: 'Аналитика банка',
  AI_INFERENCE: 'Вывод AI',
  FORECAST: 'Сценарий',
};
const reasonLabels: Record<NonNullable<FeedbackInput['reason']>, string> = {
  IRRELEVANT: 'Не относится к моей компании',
  INCORRECT_CONCLUSION: 'Некорректный вывод',
  POOR_SOURCES: 'Недостаточно подтверждений',
  TOO_GENERIC: 'Слишком общий ответ',
  OTHER: 'Другое',
};
const date = (value: string) =>
  new Intl.DateTimeFormat('ru-RU', {
    day: 'numeric',
    month: 'long',
    timeZone: 'Europe/Moscow',
  }).format(new Date(value));
const isAbort = (error: unknown) => error instanceof Error && error.name === 'AbortError';
const messageOf = (error: unknown) =>
  error instanceof Error ? error.message : 'Не удалось выполнить действие.';

function Busy({ children }: { children: ReactNode }) {
  return (
    <div className="busy" role="status">
      <Spinner visible preset={24} />
      <span>{children}</span>
    </div>
  );
}

function ErrorState({ error, retry }: { error: unknown; retry?: () => void }) {
  const data = error instanceof ApiError ? error.data : undefined;
  const title =
    data?.code === 'UNAUTHENTICATED'
      ? 'Нужен вход в сервис'
      : data?.code === 'PROFILE_NOT_READY'
        ? 'Профиль компании готовится'
        : data?.code === 'FORBIDDEN_CONTEXT' || data?.code === 'FORBIDDEN'
          ? 'Нет доступа'
          : 'Не удалось загрузить данные';
  return (
    <div className="state-card" role="alert">
      <CircleHelp size={28} />
      <h3>{title}</h3>
      <p>{messageOf(error)}</p>
      {retry && data?.retryable !== false && (
        <Button size={40} view="secondary" onClick={retry} leftAddons={<RefreshCw size={16} />}>
          Повторить
        </Button>
      )}
    </div>
  );
}

export function App({
  api,
  demo,
  onDemoExit,
}: {
  api: Api;
  demo: boolean;
  onDemoExit?: () => void;
}) {
  const [user, setUser] = useState<User>();
  const [contexts, setContexts] = useState<AvailableContext[]>();
  const [contextId, setContextId] = useState('');
  const [error, setError] = useState<unknown>();
  const [attempt, setAttempt] = useState(0);
  const [about, setAbout] = useState(false);
  const operationKeys = useRef(new Map<string, string>());
  useEffect(() => {
    const controller = new AbortController();
    setError(undefined);
    setContexts(undefined);
    Promise.all([api.me(controller.signal), api.contexts(controller.signal)])
      .then(([me, available]) => {
        if (controller.signal.aborted) return;
        setUser(me);
        setContexts(available);
        setContextId(available[0]?.context.context_id ?? '');
      })
      .catch((err) => {
        if (!isAbort(err)) setError(err);
      });
    return () => controller.abort();
  }, [api, attempt]);
  const context = contexts?.find((entry) => entry.context.context_id === contextId);
  return (
    <div className="app-shell">
      <a className="skip-link" href="#feed">
        К новостной ленте
      </a>
      <header className="topbar">
        <a className="brand" href="/" aria-label="Альфа-Бизнес, на главный экран">
          <span className="brand-mark">
            А<span />
          </span>
          <span>
            Альфа-Бизнес<small>Прототип · Burmalda</small>
          </span>
        </a>
        <div className="topbar-right">
          {demo && (
            <span className="demo-badge">
              <span />
              Деморежим · синтетические данные
            </span>
          )}
          <span className="user-name">{user?.display_name ?? 'Личный кабинет'}</span>
          {demo && onDemoExit && (
            <button className="demo-exit" type="button" onClick={onDemoExit}>
              Выйти
            </button>
          )}
          <span className="avatar" aria-hidden="true">
            {user?.display_name
              ?.split(' ')
              .map((part) => part[0])
              .slice(0, 2)
              .join('') ?? 'БП'}
          </span>
        </div>
      </header>
      <div className="app-body">
        <aside className="sidebar" aria-label="Навигация">
          <div className="sidebar-label">МОЙ БИЗНЕС</div>
          <a className="nav-item active" href="#feed">
            <Newspaper size={20} />
            Бизнес-пульс
            <span className="nav-dot" />
          </a>
          <div className="sidebar-bottom">
            <div className="sidebar-note">
              <ShieldCheck size={22} />
              <strong>У каждого вывода есть основание</strong>
              <p>Проверяйте источники и признаки компании в подробном разборе.</p>
            </div>
            <button className="nav-item" onClick={() => setAbout(true)}>
              <CircleHelp size={20} />О сервисе
            </button>
          </div>
        </aside>
        <main className="main-content">
          <div className="breadcrumbs">
            Мой бизнес <span>/</span> Бизнес-пульс
          </div>
          <div className="page-heading">
            <div>
              <div className="eyebrow">СОБЫТИЯ В КОНТЕКСТЕ ВАШЕГО БИЗНЕСА</div>
              <Typography.Title tag="h1" view="large" font="system" className="page-title">
                Бизнес-пульс
              </Typography.Title>
              <p>Что меняется на рынке — и что это значит для вашей компании.</p>
            </div>
            <span className="heading-symbol" aria-hidden="true">
              <ArrowUpRight size={40} />
            </span>
          </div>
          {error ? (
            <ErrorState error={error} retry={() => setAttempt((value) => value + 1)} />
          ) : !contexts ? (
            <Busy>Получаем доступные компании…</Busy>
          ) : contexts.length === 0 ? (
            <div className="state-card">
              <Building2 size={28} />
              <h3>Нет доступных компаний</h3>
              <p>
                Обратитесь к ответственному за доступ к интернет-банку. Связь с компанией сама по
                себе не предоставляет доступ.
              </p>
            </div>
          ) : (
            <>
              <section id="company" className="context-bar" aria-label="Бизнес-контекст">
                <div className="context-icon">
                  <Building2 size={22} />
                </div>
                <label className="context-select">
                  <span>Компания и ракурс анализа</span>
                  <select
                    aria-label="Компания и ракурс анализа"
                    value={contextId}
                    onChange={(event) => setContextId(event.target.value)}
                  >
                    {contexts.map((item) => (
                      <option key={item.context.context_id} value={item.context.context_id}>
                        {item.display_name}
                      </option>
                    ))}
                  </select>
                  <ChevronDown size={17} aria-hidden="true" />
                </label>
                <div className="context-profile">
                  <span className="status-dot" />
                  {context?.profile ? 'Профиль доступен' : 'Профиль уточняется'}
                  <small>
                    {context?.profile
                      ? `Версия ${context.profile.version}`
                      : 'Часть данных может отсутствовать'}
                  </small>
                </div>
              </section>
              {context && (
                <Workspace
                  key={context.context.context_id}
                  api={api}
                  demo={demo}
                  context={context}
                  userId={user!.user_id}
                  operationKeys={operationKeys.current}
                />
              )}
            </>
          )}
          <footer className="page-footer">
            <span>Бизнес-пульс · прототип хакатона</span>
            <span>Факты, аналитика и выводы — с проверяемыми основаниями</span>
          </footer>
        </main>
      </div>
      <Burmalda />
      {about && <AboutDialog onClose={() => setAbout(false)} demo={demo} />}
    </div>
  );
}

function Workspace({
  api,
  demo,
  context,
  userId,
  operationKeys,
}: {
  api: Api;
  demo: boolean;
  context: AvailableContext;
  userId: string;
  operationKeys: Map<string, string>;
}) {
  const [feed, setFeed] = useState<Page<FeedItem>>();
  const [error, setError] = useState<unknown>();
  const [attempt, setAttempt] = useState(0);
  const [topic, setTopic] = useState('');
  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<FeedItem>();
  const [loadingMore, setLoadingMore] = useState(false);
  const controllerRef = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    controllerRef.current = controller;
    setFeed(undefined);
    setSelected(undefined);
    setError(undefined);
    setLoadingMore(false);
    api
      .feed(context.context.context_id, topic || undefined, undefined, controller.signal)
      .then((page) => {
        if (!controller.signal.aborted) setFeed(page);
      })
      .catch((err) => {
        if (!isAbort(err)) setError(err);
      });
    return () => controller.abort();
  }, [api, context.context.context_id, topic, attempt]);
  async function more() {
    if (!feed?.next_cursor || loadingMore) return;
    setLoadingMore(true);
    setError(undefined);
    const signal = controllerRef.current!.signal;
    try {
      const page = await api.feed(
        context.context.context_id,
        topic || undefined,
        feed.next_cursor,
        signal,
      );
      if (!signal.aborted)
        setFeed({
          items: [
            ...feed.items,
            ...page.items.filter(
              (item) => !feed.items.some((old) => old.preview_id === item.preview_id),
            ),
          ],
          next_cursor: page.next_cursor,
        });
    } catch (err) {
      if (!isAbort(err)) setError(err);
    } finally {
      if (!signal.aborted) setLoadingMore(false);
    }
  }
  const visible = feed?.items.filter((item) =>
    `${item.event.title} ${item.event.summary}`
      .toLocaleLowerCase('ru')
      .includes(search.toLocaleLowerCase('ru')),
  );
  return (
    <section id="feed" className={`workspace ${selected ? 'has-selection' : ''}`}>
      <div className="feed-panel">
        <div className="section-heading">
          <div>
            <h2>В фокусе</h2>
            <span>События, которые стоит учесть</span>
          </div>
          <Button
            size={32}
            view="transparent"
            aria-label="Обновить ленту"
            onClick={() => setAttempt((value) => value + 1)}
          >
            <RefreshCw size={17} />
          </Button>
        </div>
        <Input
          block
          size={48}
          aria-label="Поиск по загруженным событиям"
          placeholder="Поиск по загруженным событиям"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          leftAddons={<Search size={18} />}
        />
        <div className="topic-list" aria-label="Темы">
          <button className={!topic ? 'topic selected' : 'topic'} onClick={() => setTopic('')}>
            Все события
          </button>
          {Object.entries(topics).map(([id, label]) => (
            <button
              key={id}
              className={topic === id ? 'topic selected' : 'topic'}
              onClick={() => setTopic(id)}
            >
              {label}
            </button>
          ))}
        </div>
        {!!error && <ErrorState error={error} retry={() => setAttempt((value) => value + 1)} />}
        {!feed && !error && <Busy>Подбираем события для компании…</Busy>}
        {visible?.length === 0 && (
          <div className="state-card">
            <Search size={26} />
            <h3>{search ? 'Ничего не найдено' : 'Пока нет релевантных событий'}</h3>
            <p>
              {search
                ? 'Измените запрос или тему.'
                : 'Здесь появятся события, связанные с выбранным бизнес-контекстом.'}
            </p>
            {search && (
              <Button size={40} onClick={() => setSearch('')}>
                Сбросить поиск
              </Button>
            )}
          </div>
        )}
        <div className="feed-list">
          {visible?.map((item, index) => (
            <button
              key={item.preview_id}
              className={`event-card ${selected?.preview_id === item.preview_id ? 'selected' : ''}`}
              onClick={() => setSelected(item)}
              aria-pressed={selected?.preview_id === item.preview_id}
            >
              <div className="event-meta">
                <span
                  className={`impact ${item.topic === 'liquidity' ? 'positive' : item.topic === 'regulation' ? 'neutral' : ''}`}
                >
                  {item.topic === 'liquidity' ? (
                    <ArrowUpRight size={13} />
                  ) : (
                    <span className="impact-dot" />
                  )}
                  {item.impact.label}
                </span>
                <span>{date(item.published_at)}</span>
              </div>
              <h3>{item.event.title}</h3>
              <p className="event-summary">
                {item.event.summary.replace('Учебный сценарий: ', '')}
              </p>
              <div className="relevance">
                <Sparkles size={15} />
                <span>{item.why_relevant}</span>
              </div>
              <div className="event-footer">
                <span>
                  {topics[item.topic] ?? item.topic} <span>·</span> {item.sources.length}{' '}
                  {item.sources.length === 1
                    ? 'источник'
                    : item.sources.length < 5
                      ? 'источника'
                      : 'источников'}
                </span>
                <span className="open-analysis">
                  Разбор <ArrowUpRight size={15} />
                </span>
              </div>
              {index === 0 && <span className="card-priority" aria-hidden="true" />}
            </button>
          ))}
        </div>
        {feed?.next_cursor && (
          <Button block size={40} loading={loadingMore} disabled={loadingMore} onClick={more}>
            Показать еще
          </Button>
        )}
        {feed && (
          <p className="feed-caption">
            <Layers3 size={14} />
            Сортировка по значимости для выбранного контекста
          </p>
        )}
      </div>
      {selected ? (
        <InsightPanel
          key={selected.preview_id}
          api={api}
          demo={demo}
          item={selected}
          context={context}
          userId={userId}
          operationKeys={operationKeys}
          onClose={() => setSelected(undefined)}
        />
      ) : (
        <div className="welcome-panel">
          <div className="welcome-top">
            <span className="small-label">ПЕРСОНАЛЬНАЯ АНАЛИТИКА</span>
            <span className="welcome-icon">
              <Sparkles size={23} />
            </span>
          </div>
          <div className="welcome-art" aria-hidden="true">
            <span className="art-orbit one" />
            <span className="art-orbit two" />
            <div className="art-document">
              <span />
              <span />
              <span />
              <div>
                <ArrowUpRight size={38} />
              </div>
            </div>
            <span className="art-check">
              <Check size={21} />
            </span>
          </div>
          <h2>
            За новостью —<br />
            влияние на ваш бизнес
          </h2>
          <p>
            Выберите событие слева. Мы свяжем его с контекстом компании и покажем, на чем основаны
            выводы.
          </p>
          <div className="welcome-features">
            <span>
              <Building2 size={17} />
              Учитываем особенности компании
            </span>
            <span>
              <FileText size={17} />
              Используем аналитику банка
            </span>
            <span>
              <ShieldCheck size={17} />
              Показываем источники и ограничения
            </span>
          </div>
          <div className="welcome-note">
            <span className="status-dot" />
            Подробный разбор готовится при открытии события
          </div>
        </div>
      )}
    </section>
  );
}

function InsightPanel({
  api,
  demo,
  item,
  context,
  userId,
  operationKeys,
  onClose,
}: {
  api: Api;
  demo: boolean;
  item: FeedItem;
  context: AvailableContext;
  userId: string;
  operationKeys: Map<string, string>;
  onClose: () => void;
}) {
  const [insight, setInsight] = useState<Insight>();
  const [error, setError] = useState<unknown>();
  const [attempt, setAttempt] = useState(0);
  const [phase, setPhase] = useState('Проверяем контекст и подбираем подтверждения…');
  const [tab, setTab] = useState<'analysis' | 'sources' | 'questions'>('analysis');
  const [focusedEvidence, setFocusedEvidence] = useState<string>();
  const panelRef = useRef<HTMLElement>(null);
  useEffect(() => {
    const controller = new AbortController();
    setError(undefined);
    setInsight(undefined);
    const scope = `${userId}:${context.context.context_id}:${context.profile?.version ?? 'unknown'}:${item.event.event_id}`;
    if (!operationKeys.has(scope)) operationKeys.set(scope, crypto.randomUUID());
    async function load() {
      let result = await api.analyse(
        context.context.context_id,
        item.event.event_id,
        operationKeys.get(scope)!,
        controller.signal,
      );
      if ('generation_id' in result) {
        const done = await waitForGeneration(api, result, controller.signal, (state) =>
          setPhase(
            state === 'QUEUED'
              ? 'Запрос принят. Ожидаем начала анализа…'
              : 'Сопоставляем данные и проверяем выводы…',
          ),
        );
        if (!done.insight_id)
          throw new ApiError(
            done.error ?? {
              code: 'INSUFFICIENT_EVIDENCE',
              message: 'Недостаточно подтверждений для подготовки разбора.',
              retryable: false,
            },
          );
        result = await api.insight(done.insight_id, controller.signal);
      }
      if (
        result.context.context_id !== context.context.context_id ||
        result.primary_event.event_id !== item.event.event_id
      )
        throw new ApiError({
          code: 'CONTEXT_MISMATCH',
          message: 'Получен ответ для другого контекста. Он не будет показан.',
          retryable: false,
        });
      if (!controller.signal.aborted) setInsight(result);
    }
    load().catch((err) => {
      if (!isAbort(err)) setError(err);
    });
    return () => controller.abort();
  }, [api, context, item, userId, operationKeys, attempt]);
  function showEvidence(id: string) {
    setFocusedEvidence(id);
    setTab('sources');
  }
  useEffect(() => {
    if (tab === 'sources' && focusedEvidence)
      panelRef.current
        ?.querySelector(`[data-evidence-id="${CSS.escape(focusedEvidence)}"]`)
        ?.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  }, [tab, focusedEvidence]);
  return (
    <article className="insight-panel" ref={panelRef} aria-label="Подробный разбор события">
      <div className="insight-toolbar">
        <Button size={32} view="transparent" leftAddons={<ArrowLeft size={15} />} onClick={onClose}>
          К ленте
        </Button>
        <span>
          <Clock3 size={14} />
          {date(item.published_at)}
        </span>
      </div>
      <div className="insight-heading">
        <span className="small-label">{topics[item.topic] ?? item.topic}</span>
        <Typography.Title tag="h2" view="medium" font="system" className="insight-title">
          {item.event.title}
        </Typography.Title>
        <div className="insight-for">
          <Building2 size={15} />
          <span>{context.display_name}</span>
        </div>
      </div>
      <div className="tabs" role="tablist" aria-label="Разделы разбора">
        {(
          [
            ['analysis', 'Разбор'],
            ['sources', `Основания${insight ? ` · ${insight.response.evidence.length}` : ''}`],
            ['questions', 'Уточнить'],
          ] as const
        ).map(([id, label]) => (
          <button
            key={id}
            id={`tab-${id}`}
            type="button"
            role="tab"
            tabIndex={tab === id ? 0 : -1}
            aria-controls={`panel-${id}`}
            aria-selected={tab === id}
            className={tab === id ? 'tab active' : 'tab'}
            onClick={() => setTab(id)}
            onKeyDown={(event) => {
              const ids = ['analysis', 'sources', 'questions'] as const;
              const index = ids.indexOf(id);
              const next =
                event.key === 'ArrowRight'
                  ? ids[(index + 1) % ids.length]
                  : event.key === 'ArrowLeft'
                    ? ids[(index + ids.length - 1) % ids.length]
                    : event.key === 'Home'
                      ? ids[0]
                      : event.key === 'End'
                        ? ids[ids.length - 1]
                        : undefined;
              if (next) {
                event.preventDefault();
                setTab(next);
                document.getElementById(`tab-${next}`)?.focus();
              }
            }}
          >
            {id === 'questions' && <MessageSquare size={15} />}
            {label}
          </button>
        ))}
      </div>
      <div
        className="insight-content"
        role="tabpanel"
        id={`panel-${tab}`}
        aria-labelledby={`tab-${tab}`}
      >
        {error ? (
          <ErrorState error={error} retry={() => setAttempt((value) => value + 1)} />
        ) : !insight ? (
          <div className="analysis-loading">
            <Busy>{phase}</Busy>
            <p>
              Результат появится здесь. При смене компании текущий ответ не будет показан в новом
              контексте.
            </p>
          </div>
        ) : (
          <>
            {
              <div hidden={tab !== 'analysis'}>
                <div className="analysis-intro">
                  <ShieldCheck size={17} />
                  <span>Факты, позиция банка и выводы AI показаны отдельно</span>
                </div>
                <ResponseView response={insight.response} demo={demo} onEvidence={showEvidence} />
                <TraceView api={api} traceId={insight.response.trace_id} />
                <FeedbackForm
                  key={`${insight.insight_id}:${insight.revision}`}
                  api={api}
                  insight={insight}
                  demo={demo}
                />
              </div>
            }
            <div hidden={tab !== 'sources'}>
              <EvidenceView
                response={insight.response}
                item={item}
                focused={focusedEvidence}
                demo={demo}
              />
            </div>
            <div hidden={tab !== 'questions'}>
              <QuestionPanel api={api} insight={insight} demo={demo} />
            </div>
          </>
        )}
      </div>
      {insight && (
        <div className="insight-bottom">
          <CheckCheck size={15} />
          <span>Разбор сохранен · версия {insight.revision}</span>
          <span>{demo ? 'Демонстрация' : date(insight.created_at)}</span>
        </div>
      )}
    </article>
  );
}

function ResponseView({
  response,
  demo,
  onEvidence,
}: {
  response: AgentResponse;
  demo: boolean;
  onEvidence?: (id: string) => void;
}) {
  const claims = visibleClaims(response);
  return (
    <>
      {response.sections.map((section, index) => {
        const entries = section.claim_ids
          .map((id) => claims.find((claim) => claim.claim_id === id))
          .filter((claim): claim is Claim => !!claim);
        if (!entries.length) return null;
        const bank = section.section_type === 'BANK_VIEW';
        return (
          <section
            className={`analysis-section ${bank ? 'bank-section' : ''}`}
            key={`${section.section_type}-${index}`}
          >
            <div className="analysis-section-title">
              <span>
                {bank ? (
                  <span className="mini-alfa">А</span>
                ) : section.section_type === 'RISKS' ? (
                  <ArrowDownRight size={18} />
                ) : section.section_type === 'SCENARIOS_ACTIONS' ? (
                  <CheckCheck size={18} />
                ) : (
                  <span className="section-indicator" />
                )}
              </span>
              <h3>{sectionLabels[section.section_type]}</h3>
              {bank && demo && <span className="synthetic-label">Учебный пример</span>}
            </div>
            {entries.map((claim) => (
              <div className="claim" key={claim.claim_id}>
                <span className={`claim-kind kind-${claim.claim_type.toLowerCase()}`}>
                  {kindLabels[claim.claim_type]}
                  {claim.verification_status === 'PARTIALLY_SUPPORTED' && ' · с оговорками'}
                </span>
                <p>{claim.text}</p>
                {claim.assumptions.length > 0 && (
                  <div className="assumptions">
                    {claim.assumptions.map((text) => (
                      <span key={text}>{text}</span>
                    ))}
                  </div>
                )}
                {onEvidence && claim.evidence_links.length > 0 && (
                  <div className="claim-links">
                    {claim.evidence_links.map((link, i) => (
                      <button
                        key={`${link.evidence_id}-${i}`}
                        onClick={() => onEvidence(link.evidence_id)}
                      >
                        <FileText size={12} />
                        {link.relation === 'CONTRADICT'
                          ? 'Противоречие'
                          : link.relation === 'CONTEXT'
                            ? 'Контекст'
                            : 'Источник'}{' '}
                        {i + 1}
                      </button>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </section>
        );
      })}
      {!response.sections.length &&
        claims.map((claim) => (
          <div className="claim" key={claim.claim_id}>
            <span className="claim-kind">{kindLabels[claim.claim_type]}</span>
            <p>{claim.text}</p>
          </div>
        ))}
      {response.limitations.length > 0 && (
        <aside className="limitations">
          <CircleHelp size={18} />
          <div>
            <strong>Что пока неизвестно</strong>
            {response.limitations.map((entry, index) => (
              <p key={`${entry.code}-${index}`}>{entry.message}</p>
            ))}
          </div>
        </aside>
      )}
    </>
  );
}

function EvidenceView({
  response,
  item,
  focused,
  demo,
}: {
  response: AgentResponse;
  item: FeedItem;
  focused?: string;
  demo: boolean;
}) {
  return (
    <div className="evidence-list">
      <p className="muted">Материалы и признаки, использованные в этой версии разбора.</p>
      {demo && (
        <div className="demo-source-note">
          Все выдержки ниже синтетические. Они показывают устройство интерфейса и не являются
          публикациями Альфа-Банка.
        </div>
      )}
      {response.evidence.map((evidence: Evidence, index) => {
        const company = evidence.evidence_type === 'COMPANY_DATA';
        const source = !company
          ? item.sources.find((entry) => entry.source_id === evidence.reference.source_id)
          : undefined;
        const url = safeExternalUrl(source?.url);
        return (
          <section
            key={evidence.evidence_id}
            data-evidence-id={evidence.evidence_id}
            className={`evidence-card ${focused === evidence.evidence_id ? 'focused' : ''}`}
          >
            <div className="evidence-top">
              <span className="evidence-number">{index + 1}</span>
              <span>
                {company
                  ? 'Данные компании'
                  : evidence.evidence_type === 'ALFA_ANALYTICS'
                    ? 'Аналитика банка'
                    : 'Внешний материал'}
              </span>
            </div>
            <h3>{company ? 'Признаки бизнес-профиля' : (source?.name ?? 'Материал источника')}</h3>
            <blockquote>{company ? evidence.content : evidence.excerpt}</blockquote>
            <div className="evidence-version">
              <Layers3 size={13} />
              {company
                ? `Профиль · версия ${evidence.reference.profile.version}`
                : `Версия документа: ${evidence.reference.document_version_id}`}
            </div>
            {url && (
              <a className="text-link" href={url} target="_blank" rel="noopener noreferrer">
                Открыть источник <ArrowUpRight size={14} />
              </a>
            )}
            {!company && !url && (
              <p className="source-unavailable">
                {demo
                  ? 'В деморежиме оригинал не публикуется.'
                  : 'Ссылка на оригинал не передана сервером. Доступна сохраненная выдержка.'}
              </p>
            )}
          </section>
        );
      })}
    </div>
  );
}

function TraceView({ api, traceId }: { api: Api; traceId: string }) {
  const [open, setOpen] = useState(false);
  const [trace, setTrace] = useState<Trace>();
  const [error, setError] = useState<unknown>();
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setError(undefined);
    api
      .trace(traceId, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setTrace(value);
      })
      .catch((err) => {
        if (!isAbort(err)) setError(err);
      });
    return () => controller.abort();
  }, [api, traceId, open]);
  return (
    <div className="trace">
      <button className="trace-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
        <ShieldCheck size={16} />
        Как подготовлен разбор
        <ChevronDown className={open ? 'rotated' : ''} size={16} />
      </button>
      {open && (
        <div className="trace-body">
          {error ? (
            <p role="alert">{messageOf(error)}</p>
          ) : !trace ? (
            <Busy>Получаем этапы анализа…</Busy>
          ) : (
            trace.stages.map((stage) => (
              <div className="trace-stage" key={stage.stage_id}>
                <Check size={14} />
                <span>
                  {stage.summary ?? stage.stage_type}
                  <small>{stage.status === 'SUCCEEDED' ? 'Завершено' : stage.status}</small>
                </span>
              </div>
            ))
          )}
        </div>
      )}
    </div>
  );
}

function FeedbackForm({ api, insight, demo }: { api: Api; insight: Insight; demo: boolean }) {
  const [rating, setRating] = useState<FeedbackInput['rating']>();
  const [negative, setNegative] = useState(false);
  const [reason, setReason] = useState<FeedbackInput['reason']>('TOO_GENERIC');
  const [comment, setComment] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), []);
  async function submit(next: FeedbackInput['rating']) {
    if (busy) return;
    const current = new AbortController();
    controller.current = current;
    setBusy(true);
    setError('');
    try {
      await api.feedback(
        insight.insight_id,
        {
          insight_revision: insight.revision,
          rating: next,
          ...(next === 'NEGATIVE'
            ? { reason, ...(comment.trim() ? { comment: comment.trim() } : {}) }
            : {}),
        },
        current.signal,
      );
      if (!current.signal.aborted) {
        setRating(next);
        setNegative(false);
      }
    } catch (err) {
      if (!isAbort(err)) setError(messageOf(err));
    } finally {
      if (!current.signal.aborted) setBusy(false);
    }
  }
  return (
    <section className="feedback">
      <div className="feedback-heading">
        <div>
          <strong>Этот разбор был полезен?</strong>
          <p>
            {rating
              ? demo
                ? 'Оценка сохранена локально в деморежиме'
                : 'Спасибо, оценка сохранена'
              : 'Помогите сделать аналитику точнее'}
          </p>
        </div>
        <div className="feedback-buttons">
          <Button
            size={40}
            view={rating === 'POSITIVE' ? 'primary' : 'secondary'}
            aria-label="Разбор полезен"
            aria-pressed={rating === 'POSITIVE'}
            disabled={busy}
            onClick={() => submit('POSITIVE')}
          >
            <ThumbsUp size={17} />
          </Button>
          <Button
            size={40}
            view={rating === 'NEGATIVE' ? 'primary' : 'secondary'}
            aria-label="Разбор не полезен"
            aria-pressed={rating === 'NEGATIVE'}
            disabled={busy}
            onClick={() => setNegative(!negative)}
          >
            <ThumbsDown size={17} />
          </Button>
        </div>
      </div>
      {negative && (
        <form
          className="feedback-details"
          onSubmit={(event) => {
            event.preventDefault();
            void submit('NEGATIVE');
          }}
        >
          <label>
            Что можно улучшить?
            <select
              aria-label="Причина отрицательной оценки"
              value={reason}
              onChange={(event) => setReason(event.target.value as FeedbackInput['reason'])}
            >
              {Object.entries(reasonLabels).map(([id, text]) => (
                <option key={id} value={id}>
                  {text}
                </option>
              ))}
            </select>
          </label>
          <Textarea
            block
            label="Комментарий, необязательно"
            value={comment}
            maxLength={2000}
            onChange={(_, payload) => setComment(payload.value)}
          />
          <Button type="submit" size={40} view="primary" loading={busy} disabled={busy}>
            Отправить оценку
          </Button>
        </form>
      )}
      {error && (
        <p className="inline-error" role="alert">
          {error}
        </p>
      )}
    </section>
  );
}

function QuestionPanel({ api, insight, demo }: { api: Api; insight: Insight; demo: boolean }) {
  const [conversation, setConversation] = useState<Conversation>();
  const [messages, setMessages] = useState<Message[]>();
  const [text, setText] = useState('');
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const lifetime = useRef<AbortController | null>(null);
  const pending = useRef<{ text: string; key: string } | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    setError(undefined);
    async function load() {
      const page = await api.conversations(insight.insight_id, controller.signal);
      const existing = page.items.find((entry) => entry.insight_revision === insight.revision);
      if (existing) {
        const history = await loadMessages(api, existing.conversation_id, controller.signal);
        if (!controller.signal.aborted) {
          setConversation(existing);
          setMessages(history);
        }
      } else if (!controller.signal.aborted) setMessages([]);
    }
    load().catch((err) => {
      if (!isAbort(err)) setError(err);
    });
    return () => controller.abort();
  }, [api, insight.insight_id, insight.revision, attempt]);
  async function send() {
    const question = text.trim();
    if (!question || busy || !messages) return;
    const signal = lifetime.current!.signal;
    setBusy(true);
    setError(undefined);
    if (!pending.current || pending.current.text !== question)
      pending.current = { text: question, key: crypto.randomUUID() };
    try {
      const active = conversation ?? (await api.createConversation(insight.insight_id, signal));
      if (signal.aborted) return;
      setConversation(active);
      const result = await api.ask(active.conversation_id, question, pending.current.key, signal);
      let history: Message[];
      if ('generation_id' in result) {
        const done = await waitForGeneration(api, result, signal);
        if (done.state === 'INSUFFICIENT_EVIDENCE' && !done.message_id)
          throw new ApiError({
            code: 'INSUFFICIENT_EVIDENCE',
            message: 'Недостаточно данных для уточнения. Исходный разбор сохранен.',
            retryable: false,
          });
        history = await loadMessages(api, active.conversation_id, signal);
      } else
        history = [
          ...messages.filter(
            (entry) =>
              entry.message_id !== result.user_message.message_id &&
              entry.message_id !== result.assistant_message.message_id,
          ),
          result.user_message,
          result.assistant_message,
        ].sort((a, b) => a.sequence - b.sequence);
      if (!signal.aborted) {
        setMessages(history);
        setText('');
        pending.current = null;
      }
    } catch (err) {
      if (!isAbort(err)) setError(err);
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  }
  return (
    <section className="question-panel">
      <div className="question-context">
        <MessageSquare size={19} />
        <div>
          <strong>Обсудим этот разбор</strong>
          <p>Вопросы сохраняют текущую компанию, роль и версию анализа.</p>
        </div>
      </div>
      {!messages && !error && <Busy>Загружаем историю диалога…</Busy>}
      {messages?.length === 0 && (
        <div className="question-empty">
          <h3>Что стоит уточнить?</h3>
          <p>Попросите объяснить связь с вашей компанией или перечислить недостающие данные.</p>
          <div className="suggestions">
            {[
              'Какие данные нужны для точной оценки?',
              'На чем основан этот вывод?',
              'Что проверить в первую очередь?',
            ].map((question) => (
              <button key={question} onClick={() => setText(question)}>
                {question}
                <ArrowRight size={14} />
              </button>
            ))}
          </div>
        </div>
      )}
      <div className="messages">
        {messages?.map((entry) => (
          <div
            key={entry.message_id}
            className={`message ${entry.author === 'USER' ? 'from-user' : 'from-assistant'}`}
          >
            <span className="message-author">
              {entry.author === 'USER' ? 'Вы' : demo ? 'Учебный ответ' : 'AI-аналитик'}
            </span>
            {entry.author === 'USER' ? (
              <p>{entry.text}</p>
            ) : (
              entry.response && <ResponseView response={entry.response} demo={demo} />
            )}
          </div>
        ))}
      </div>
      {!!error && (
        <ErrorState
          error={error}
          retry={!messages ? () => setAttempt((value) => value + 1) : undefined}
        />
      )}
      {messages && (
        <form
          className="question-form"
          onSubmit={(event) => {
            event.preventDefault();
            void send();
          }}
        >
          <Textarea
            block
            autosize
            minRows={2}
            maxRows={6}
            maxLength={4000}
            aria-label="Уточняющий вопрос"
            placeholder="Задайте вопрос по этому событию…"
            value={text}
            onChange={(_, payload) => setText(payload.value)}
            disabled={busy}
          />
          <div className="question-form-footer">
            <span>
              {demo ? 'Демонстрация без вызова модели' : 'Ответ использует контекст этого разбора'}
            </span>
            <Button
              type="submit"
              view="primary"
              size={40}
              disabled={busy || !text.trim()}
              loading={busy}
              rightAddons={<Send size={15} />}
            >
              Отправить
            </Button>
          </div>
        </form>
      )}
    </section>
  );
}

function AboutDialog({ onClose, demo }: { onClose: () => void; demo: boolean }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    ref.current?.showModal();
  }, []);
  return (
    <dialog
      className="about-dialog"
      ref={ref}
      onCancel={onClose}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className="dialog-heading">
        <h2>О Бизнес-пульсе</h2>
        <Button view="transparent" size={32} aria-label="Закрыть окно" onClick={onClose}>
          <X size={18} />
        </Button>
      </div>
      <p>
        Прототип персональной новостной аналитики команды Burmalda. Сервис связывает события с
        признаками компании и показывает основания выводов.
      </p>
      <p>
        Интерфейс использует библиотеку Альфа-Банка Core Components. Прототип не является
        действующим интернет-банком.
      </p>
      {demo && (
        <div className="demo-source-note">
          Сейчас включен демонстрационный режим. Компании, события, аналитика и ответы
          синтетические. Данные диалога и оценки хранятся в этой вкладке браузера.
        </div>
      )}
      <Button view="primary" size={48} block onClick={onClose}>
        Понятно
      </Button>
    </dialog>
  );
}
