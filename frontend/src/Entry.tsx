import { useEffect, useMemo, useRef, useState } from 'react';
import { Button } from '@alfalab/core-components-button';
import {
  ArrowRight,
  ArrowUpRight,
  Bell,
  Building2,
  Check,
  Clock3,
  CreditCard,
  FileText,
  LayoutDashboard,
  LockKeyhole,
  ShieldCheck,
  Sparkles,
  TrendingUp,
  Wallet,
  X,
} from 'lucide-react';
import { App } from './App';
import { ApiError, type Api } from './api';
import {
  authenticateDemo,
  clearDemoSession,
  demoAccounts,
  demoCompanies,
  demoPositions,
  readDemoSession,
  saveDemoSession,
  scopedDemoApi,
  type DemoSession,
} from './demo-auth';
import './entry.css';

type AuthState = 'checking' | 'ready' | 'unauthenticated' | 'error';

function authDestination(value: string): string | undefined {
  if (!value.trim()) return undefined;
  try {
    const url = new URL(value, window.location.origin);
    if (url.username || url.password) return undefined;
    if (url.protocol !== 'https:' && url.origin !== window.location.origin) return undefined;
    return url.href;
  } catch {
    return undefined;
  }
}

export function Entry({
  api,
  demo,
  authStartUrl = '',
}: {
  api: Api;
  demo: boolean;
  authStartUrl?: string;
}) {
  const [demoSession, setDemoSession] = useState<DemoSession | undefined>(() =>
    demo ? readDemoSession() : undefined,
  );
  const [inService, setInService] = useState(
    () => demo && window.location.pathname === '/pulse' && Boolean(readDemoSession()),
  );
  const [loginOpen, setLoginOpen] = useState(
    () => window.location.pathname === '/pulse' && (!demo || !readDemoSession()),
  );
  const [authState, setAuthState] = useState<AuthState>('checking');
  const [checkAttempt, setCheckAttempt] = useState(0);
  const destination = authDestination(authStartUrl);
  const serviceApi = useMemo(
    () => (demo && demoSession ? scopedDemoApi(api, demoSession) : api),
    [api, demo, demoSession],
  );

  useEffect(() => {
    const onPopState = () => {
      const wantsService = window.location.pathname === '/pulse';
      setInService(wantsService && demo && Boolean(readDemoSession()));
      setLoginOpen(wantsService && (!demo || !readDemoSession()));
    };
    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  }, [demo]);

  useEffect(() => {
    if (!loginOpen || demo) return;
    const controller = new AbortController();
    setAuthState('checking');
    api
      .me(controller.signal)
      .then(() => {
        if (!controller.signal.aborted) setAuthState('ready');
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setAuthState(
          error instanceof ApiError && error.status === 401 ? 'unauthenticated' : 'error',
        );
      });
    return () => controller.abort();
  }, [api, demo, loginOpen, checkAttempt]);

  function enterService() {
    if (window.location.pathname !== '/pulse') {
      window.history.pushState(null, '', `/pulse${window.location.search}`);
    }
    setLoginOpen(false);
    setInService(true);
    window.scrollTo(0, 0);
  }

  function closeLogin() {
    setLoginOpen(false);
    if (window.location.pathname === '/pulse') {
      window.history.replaceState(null, '', `/${window.location.search}`);
    }
  }

  function enterDemo(session: DemoSession) {
    saveDemoSession(session);
    setDemoSession(session);
    enterService();
  }

  function exitDemo() {
    clearDemoSession();
    setDemoSession(undefined);
    setInService(false);
    window.history.replaceState(null, '', `/${window.location.search}`);
    window.scrollTo(0, 0);
  }

  if (inService)
    return <App api={serviceApi} demo={demo} onDemoExit={demo ? exitDemo : undefined} />;

  return (
    <>
      <Landing onOpen={() => setLoginOpen(true)} demo={demo} />
      {loginOpen && (
        <LoginDialog
          demo={demo}
          state={authState}
          destination={destination}
          onClose={closeLogin}
          onRetry={() => setCheckAttempt((value) => value + 1)}
          onEnter={enterService}
          onDemoLogin={enterDemo}
        />
      )}
    </>
  );
}

function Brand({ compact = false }: { compact?: boolean }) {
  return (
    <span className={`bank-brand ${compact ? 'bank-brand-compact' : ''}`}>
      <span className="bank-brand-symbol" aria-hidden="true">
        А<span />
      </span>
      <span className="bank-brand-name">
        Альфа<span>Бизнес</span>
      </span>
    </span>
  );
}

function Landing({ onOpen, demo }: { onOpen: () => void; demo: boolean }) {
  return (
    <div className="landing-page">
      <a className="skip-link" href="#overview">
        К основному содержимому
      </a>
      <header className="bank-header">
        <div className="bank-header-inner">
          <a href="/" aria-label="Альфа-Бизнес, главная">
            <Brand />
          </a>
          <nav className="bank-header-nav" aria-label="Основная навигация">
            <a href="#overview" aria-current="page">
              Главная
            </a>
            <a href="#accounts">Счета</a>
            <a href="#for-you">Сервисы</a>
          </nav>
          <div className="bank-header-actions">
            <span className="bank-demo-label">Учебный экран</span>
            <Button size={40} view="secondary" onClick={onOpen}>
              {demo ? 'Попробовать' : 'Войти'}
            </Button>
          </div>
        </div>
      </header>

      <main id="overview" className="bank-main">
        <div className="bank-page-heading">
          <div>
            <div className="bank-eyebrow">АЛЬФА-БИЗНЕС · ДЕМО</div>
            <h1>{demo ? 'Добрый день!' : 'Добро пожаловать'}</h1>
            <p>Всё важное для бизнеса — в одном месте.</p>
          </div>
          <div className="bank-date">
            <Bell size={18} aria-hidden="true" />
            <span>Главная · обзор</span>
          </div>
        </div>

        <section className="bank-hero" aria-labelledby="pulse-title">
          <div className="bank-hero-copy">
            <span className="bank-hero-kicker">
              <Sparkles size={15} /> НОВЫЙ СЕРВИС
            </span>
            <h2 id="pulse-title">
              Будьте на шаг
              <br /> впереди событий.
            </h2>
            <p>
              Бизнес-пульс замечает новости, которые могут повлиять на вашу компанию, и показывает,
              на чём основаны выводы.
            </p>
            <button className="bank-hero-cta" type="button" onClick={onOpen}>
              <span>Открыть Бизнес-пульс</span>
              <span className="bank-hero-cta-icon">
                <ArrowUpRight size={22} />
              </span>
            </button>
            <span className="bank-hero-footnote">
              Персональная аналитика · источники и факты внутри
            </span>
          </div>
          <div className="bank-hero-art" aria-hidden="true">
            <div className="bank-orbit bank-orbit-one" />
            <div className="bank-orbit bank-orbit-two" />
            <div className="bank-signal-card bank-signal-card-back">
              <span>РЫНОК</span>
              <TrendingUp size={20} />
            </div>
            <div className="bank-signal-card bank-signal-card-front">
              <span className="bank-signal-dot" />
              <div>
                <small>БИЗНЕС-ПУЛЬС</small>
                <strong>Важное не пройдёт мимо</strong>
                <span>Новости → контекст → решение</span>
              </div>
              <ArrowUpRight size={20} />
            </div>
            <div className="bank-signal-spark bank-signal-spark-one" />
            <div className="bank-signal-spark bank-signal-spark-two" />
          </div>
        </section>

        <div className="bank-content-grid">
          <section
            className="bank-panel bank-accounts"
            id="accounts"
            aria-labelledby="accounts-title"
          >
            <div className="bank-panel-heading">
              <div>
                <span className="bank-panel-eyebrow">ФИНАНСЫ</span>
                <h2 id="accounts-title">Ваши счета</h2>
              </div>
              <span className="bank-panel-meta">Демо-данные</span>
            </div>
            <div className="bank-balance">
              <div className="bank-balance-icon">
                <Wallet size={22} />
              </div>
              <div>
                <span>Доступный остаток</span>
                <strong>12 840 500,00 ₽</strong>
              </div>
            </div>
            <div className="bank-account-row">
              <span className="bank-account-dot" />
              <span>
                Основной счёт <small>•• 4589</small>
              </span>
              <strong>9 640 500 ₽</strong>
            </div>
            <div className="bank-account-row">
              <span className="bank-account-dot bank-account-dot-dark" />
              <span>
                Резервный счёт <small>•• 0974</small>
              </span>
              <strong>3 200 000 ₽</strong>
            </div>
          </section>

          <section className="bank-panel bank-activity" aria-labelledby="activity-title">
            <div className="bank-panel-heading">
              <div>
                <span className="bank-panel-eyebrow">СЕГОДНЯ</span>
                <h2 id="activity-title">Бизнес в движении</h2>
              </div>
              <Clock3 size={20} className="bank-muted-arrow" />
            </div>
            <div className="bank-activity-row">
              <span className="bank-activity-icon">
                <CreditCard size={19} />
              </span>
              <div>
                <strong>Платежи</strong>
                <span>3 операции на согласовании</span>
              </div>
            </div>
            <div className="bank-activity-row">
              <span className="bank-activity-icon">
                <FileText size={19} />
              </span>
              <div>
                <strong>Документы</strong>
                <span>Новые документы для бизнеса</span>
              </div>
            </div>
            <div className="bank-activity-row">
              <span className="bank-activity-icon">
                <Building2 size={19} />
              </span>
              <div>
                <strong>Компания</strong>
                <span>Контекст для аналитики готов</span>
              </div>
            </div>
          </section>
        </div>

        <section className="bank-services" id="for-you" aria-labelledby="services-title">
          <div className="bank-section-title">
            <span className="bank-panel-eyebrow">ДЛЯ ВАС</span>
            <h2 id="services-title">Сервисы для роста</h2>
          </div>
          <div className="bank-service-cards">
            <div className="bank-service-card">
              <span className="bank-service-icon">
                <LayoutDashboard size={22} />
              </span>
              <div>
                <strong>Всё под контролем</strong>
                <p>Обзор финансов в удобном формате.</p>
              </div>
            </div>
            <div className="bank-service-card">
              <span className="bank-service-icon">
                <ShieldCheck size={22} />
              </span>
              <div>
                <strong>Основания решений</strong>
                <p>Проверяйте факты и первоисточники.</p>
              </div>
            </div>
            <div className="bank-service-card bank-service-card-spotlight">
              <span className="bank-service-icon">
                <Sparkles size={22} />
              </span>
              <div>
                <strong>Бизнес-пульс</strong>
                <p>Узнавайте о важных событиях раньше.</p>
              </div>
              <button type="button" onClick={onOpen} aria-label="Открыть Бизнес-пульс">
                <ArrowUpRight size={20} />
              </button>
            </div>
          </div>
        </section>
        <p className="bank-disclaimer">
          Демонстрационный интерфейс команды Burmalda. Это не действующий интернет-банк; все
          финансовые данные на экране вымышлены.
        </p>
      </main>
    </div>
  );
}

function LoginDialog({
  demo,
  state,
  destination,
  onClose,
  onRetry,
  onEnter,
  onDemoLogin,
}: {
  demo: boolean;
  state: AuthState;
  destination?: string;
  onClose: () => void;
  onRetry: () => void;
  onEnter: () => void;
  onDemoLogin: (session: DemoSession) => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [login, setLogin] = useState('');
  const [password, setPassword] = useState('');
  const [companyId, setCompanyId] = useState<string>(demoCompanies[0].id);
  const [positionId, setPositionId] = useState<string>(demoPositions[0].id);
  const [loginError, setLoginError] = useState(false);
  useEffect(() => {
    ref.current?.showModal();
  }, []);
  return (
    <dialog
      ref={ref}
      className="bank-login-dialog"
      aria-labelledby="login-title"
      aria-describedby="login-description"
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className="bank-login-art" aria-hidden="true">
        <Brand compact />
        <div className="bank-login-art-message">
          <Sparkles size={26} />
          <strong>
            Новости встречаются
            <br />с вашим бизнесом.
          </strong>
          <span>И у каждого вывода есть основание.</span>
        </div>
        <span>Бизнес-пульс · прототип</span>
      </div>
      <div className="bank-login-content">
        <button
          className="bank-login-close"
          type="button"
          onClick={onClose}
          aria-label="Закрыть окно входа"
        >
          <X size={20} />
        </button>
        <div className="bank-login-kicker">
          <LockKeyhole size={17} /> ДОСТУП К СЕРВИСУ
        </div>
        <h2 id="login-title">
          Войти в<br /> Бизнес-пульс
        </h2>
        <p id="login-description">Персональная аналитика доступна в контексте вашей компании.</p>
        {demo ? (
          <form
            className="bank-demo-form"
            onSubmit={(event) => {
              event.preventDefault();
              const session = authenticateDemo(login, password, companyId, positionId);
              if (!session) {
                setLoginError(true);
                return;
              }
              onDemoLogin(session);
            }}
          >
            <label>
              Логин
              <input
                autoComplete="username"
                name="username"
                value={login}
                onChange={(event) => {
                  setLogin(event.target.value);
                  setLoginError(false);
                }}
                required
              />
            </label>
            <label>
              Пароль
              <input
                autoComplete="current-password"
                name="password"
                type="password"
                value={password}
                onChange={(event) => {
                  setPassword(event.target.value);
                  setLoginError(false);
                }}
                required
              />
            </label>
            <div className="bank-demo-form-row">
              <label>
                Компания
                <select
                  value={companyId}
                  onChange={(event) => {
                    setCompanyId(event.target.value);
                    setLoginError(false);
                  }}
                >
                  {demoCompanies.map((company) => (
                    <option key={company.id} value={company.id}>
                      {company.name} · {company.detail}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                Должность
                <select value={positionId} onChange={(event) => setPositionId(event.target.value)}>
                  {demoPositions.map((position) => (
                    <option key={position.id} value={position.id}>
                      {position.name}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            {loginError && (
              <p className="bank-demo-error" role="alert">
                Проверьте логин, пароль, компанию и должность.
              </p>
            )}
            <Button
              size={56}
              view="primary"
              block
              type="submit"
              rightAddons={<ArrowRight size={20} />}
            >
              Войти в демо
            </Button>
            <div className="bank-demo-accounts">
              <strong>Тестовые доступы</strong>
              {demoAccounts.map((account) => (
                <button
                  key={account.login}
                  type="button"
                  onClick={() => {
                    setLogin(account.login);
                    setPassword(account.password);
                    setCompanyId(account.companyId);
                    setLoginError(false);
                  }}
                >
                  <span>
                    {demoCompanies.find((company) => company.id === account.companyId)?.name}
                  </span>
                  <code>
                    {account.login} / {account.password}
                  </code>
                </button>
              ))}
            </div>
          </form>
        ) : (
          <>
            <div className="bank-login-state" role="status" aria-live="polite">
              {state === 'checking' ? (
                <>
                  <span className="bank-login-status-icon bank-login-status-loading" />
                  <div>
                    <strong>Проверяем сессию</strong>
                    <span>Это займёт несколько секунд.</span>
                  </div>
                </>
              ) : state === 'ready' ? (
                <>
                  <span className="bank-login-status-icon">
                    <Check size={20} />
                  </span>
                  <div>
                    <strong>Сессия подтверждена</strong>
                    <span>Можно перейти к аналитике вашей компании.</span>
                  </div>
                </>
              ) : state === 'unauthenticated' ? (
                <>
                  <span className="bank-login-status-icon bank-login-status-neutral">
                    <LockKeyhole size={19} />
                  </span>
                  <div>
                    <strong>Нужен вход в Альфа-Бизнес</strong>
                    <span>
                      {destination
                        ? 'Продолжите через защищённый вход.'
                        : 'Маршрут авторизации пока не подключён. Обратитесь к администратору стенда.'}
                    </span>
                  </div>
                </>
              ) : (
                <>
                  <span className="bank-login-status-icon bank-login-status-neutral">
                    <ShieldCheck size={19} />
                  </span>
                  <div>
                    <strong>Не удалось проверить сессию</strong>
                    <span>Проверьте подключение и повторите попытку.</span>
                  </div>
                </>
              )}
            </div>
            {state === 'ready' ? (
              <Button
                size={56}
                view="primary"
                block
                onClick={onEnter}
                rightAddons={<ArrowRight size={20} />}
              >
                Перейти к аналитике
              </Button>
            ) : state === 'unauthenticated' && destination ? (
              <a className="bank-login-redirect" href={destination}>
                Продолжить к авторизации <ArrowRight size={19} />
              </a>
            ) : (
              <Button
                size={48}
                view="secondary"
                block
                disabled={state === 'checking'}
                onClick={onRetry}
              >
                Проверить ещё раз
              </Button>
            )}
          </>
        )}
        <p className="bank-login-security">
          <ShieldCheck size={17} />{' '}
          {demo
            ? 'Только тестовые данные. Пароли видны в коде и не подходят для настоящего банковского входа.'
            : 'Пароли и коды не вводятся в этот прототип. Сессию выдаёт серверный механизм входа.'}
        </p>
      </div>
    </dialog>
  );
}
