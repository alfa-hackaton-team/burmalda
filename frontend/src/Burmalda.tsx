import { useEffect, useRef, useState, type CSSProperties } from 'react';
import { RotateCw, X } from 'lucide-react';
import { safeExternalUrl } from './api';
import { newsOriginLabel, newsSummary } from './news';
import type { NewsItem } from './types';
import './burmalda.css';

export const burmaldaNews = [
  {
    id: 'night-shift',
    category: 'Бизнес',
    title: 'Ночная смена для маленьких идей',
    summary:
      'В вымышленном городе открылась мастерская, где предприниматели тестируют прототипы после рабочего дня.',
  },
  {
    id: 'smart-warehouse',
    category: 'Технологии',
    title: 'Склад научился считать свободные полки',
    summary:
      'Учебная команда показала систему, которая подсказывает, куда поставить следующую коробку.',
  },
  {
    id: 'green-roof',
    category: 'Города',
    title: 'На крыше бизнес-центра появился сад',
    summary:
      'В тестовой истории сотрудники превратили пустую крышу в пространство для встреч и городских растений.',
  },
  {
    id: 'quiet-delivery',
    category: 'Логистика',
    title: 'Утренние доставки стали тише',
    summary:
      'Вымышленный перевозчик пробует новые маршруты и электрический транспорт для коротких поездок.',
  },
  {
    id: 'repair-cafe',
    category: 'Идеи',
    title: 'Вместо свалки — мастерская ремонта',
    summary: 'Тестовый проект объединил местные сервисы, чтобы давать вещам вторую жизнь.',
  },
  {
    id: 'coffee-lab',
    category: 'Рынок',
    title: 'Кофейня открыла лабораторию вкуса',
    summary:
      'В демонстрационном сюжете гости собирают собственные смеси и голосуют за меню следующего месяца.',
  },
  {
    id: 'school-makers',
    category: 'Образование',
    title: 'Школьники собрали полезного робота',
    summary:
      'В учебной новости робот помогает сортировать материалы в мастерской и учит бережному производству.',
  },
  {
    id: 'floating-market',
    category: 'События',
    title: 'Рынок выходного дня сменил маршрут',
    summary:
      'Придуманная ярмарка путешествует по районам и знакомит жителей с локальными производителями.',
  },
] as const;

export type BurmaldaNews = {
  id: string;
  category: string;
  title: string;
  summary: string;
  sourceName?: string;
  origin?: NewsItem['text_origin'];
  url?: string;
  publishedAt?: string;
  fetchedAt?: string;
};
export const SPIN_DURATION_MS = 6400;
const NEWS_CACHE_MS = 5 * 60 * 1000;
const wheelColors = ['#ef3124', '#075af4', '#a532f1', '#b7ed00'];

export function pickRandomNews(
  stories: readonly BurmaldaNews[],
  previousId?: string,
  random = Math.random,
): BurmaldaNews | undefined {
  const choices = stories.length > 1 ? stories.filter((item) => item.id !== previousId) : stories;
  if (choices.length === 0) return undefined;
  return choices[Math.min(choices.length - 1, Math.floor(random() * choices.length))];
}

function sampleStories(stories: readonly BurmaldaNews[]): BurmaldaNews[] {
  const sample = [...stories];
  for (let index = sample.length - 1; index > 0; index--) {
    const choice = Math.floor(Math.random() * (index + 1));
    [sample[index], sample[choice]] = [sample[choice], sample[index]];
  }
  return sample.slice(0, 8);
}

function wheelGradient(count: number): string {
  const step = 360 / count;
  const sectors = Array.from({ length: count }, (_, index) => {
    const from = index * step;
    const separator = from + step - 1;
    return `${wheelColors[index % wheelColors.length]} ${from}deg ${separator}deg, #fff ${separator}deg ${from + step}deg`;
  });
  return `conic-gradient(from -${step / 2}deg, ${sectors.join(', ')})`;
}

function asStory(item: NewsItem): BurmaldaNews {
  return {
    id: item.article_id,
    category: item.source_name,
    title: item.title,
    summary: newsSummary(item),
    sourceName: item.source_name,
    origin: item.text_origin,
    url: safeExternalUrl(item.url ?? undefined),
    publishedAt: item.published_at ?? undefined,
    fetchedAt: item.fetched_at,
  };
}

function storyDate(story: BurmaldaNews): string {
  const value = story.publishedAt ?? story.fetchedAt;
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return `${story.publishedAt ? 'Опубликовано' : 'Загружено'} ${new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'long', year: 'numeric' }).format(date)} `;
}

export function Burmalda({
  demo,
  loadNews,
}: {
  demo: boolean;
  loadNews?: (signal?: AbortSignal) => Promise<NewsItem[]>;
}) {
  const dialog = useRef<HTMLDialogElement | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const request = useRef<AbortController | null>(null);
  const cache = useRef<{ stories: BurmaldaNews[]; loadedAt: number } | null>(null);
  const previousId = useRef<string | undefined>(undefined);
  const pending = useRef<BurmaldaNews | undefined>(undefined);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [stories, setStories] = useState<BurmaldaNews[]>(demo ? [...burmaldaNews] : []);
  const [spinning, setSpinning] = useState(false);
  const [selected, setSelected] = useState<BurmaldaNews>();
  const [landingIndex, setLandingIndex] = useState(0);
  const [round, setRound] = useState(0);

  useEffect(
    () => () => {
      window.clearTimeout(timer.current);
      request.current?.abort();
    },
    [],
  );
  useEffect(() => {
    if (!open) return;
    const element = dialog.current;
    element?.showModal();
    return () => {
      if (element?.open) element.close();
    };
  }, [open]);

  function startSpin(choices = stories) {
    if (spinning) return;
    const news = pickRandomNews(choices, previousId.current);
    if (!news) return;
    window.clearTimeout(timer.current);
    pending.current = news;
    setLandingIndex(choices.findIndex((item) => item.id === news.id));
    setSelected(undefined);
    setSpinning(true);
    setRound((value) => value + 1);
    const duration = window.matchMedia('(prefers-reduced-motion: reduce)').matches
      ? 80
      : SPIN_DURATION_MS;
    timer.current = window.setTimeout(() => {
      if (!pending.current) return;
      previousId.current = pending.current.id;
      setSelected(pending.current);
      setSpinning(false);
    }, duration);
  }

  async function loadStoredNews() {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoadError(false);
    setLoading(true);
    setSelected(undefined);
    setStories([]);
    try {
      const recent = await loadNews?.(controller.signal);
      if (controller.signal.aborted) return;
      if (!Array.isArray(recent)) throw new Error('Invalid news response');
      const unique = new Map<string, BurmaldaNews>();
      for (const item of recent) {
        if (!item?.article_id || !item.title?.trim()) continue;
        unique.set(item.article_id, asStory(item));
      }
      const stored = [...unique.values()];
      cache.current = { stories: stored, loadedAt: Date.now() };
      const next = sampleStories(stored);
      setStories(next);
      setLoading(false);
      startSpin(next);
    } catch {
      if (controller.signal.aborted) return;
      setLoading(false);
      setLoadError(true);
    }
  }

  function openWheel() {
    setOpen(true);
    setSelected(undefined);
    if (demo) {
      startSpin([...burmaldaNews]);
      return;
    }
    if (cache.current && Date.now() - cache.current.loadedAt < NEWS_CACHE_MS) {
      const next = sampleStories(cache.current.stories);
      setStories(next);
      startSpin(next);
      return;
    }
    void loadStoredNews();
  }

  function close() {
    request.current?.abort();
    window.clearTimeout(timer.current);
    pending.current = undefined;
    setSpinning(false);
    setSelected(undefined);
    setOpen(false);
  }

  return (
    <>
      <button
        className="burmalda-launcher"
        type="button"
        aria-label="Бурмалда — открыть колесо случайных новостей"
        title="Бурмалда · случайная новость"
        onClick={openWheel}
      >
        <span className="burmalda-mini-wheel" aria-hidden="true">
          <span>Б</span>
        </span>
      </button>

      {open && (
        <dialog
          ref={dialog}
          className="burmalda-overlay"
          aria-labelledby="burmalda-title"
          aria-describedby="burmalda-description"
          onCancel={(event) => {
            event.preventDefault();
            close();
          }}
          onClick={(event) => {
            if (event.target === event.currentTarget) close();
          }}
        >
          <div className="burmalda-overlay-inner">
            <header className="burmalda-overlay-header">
              <span className="burmalda-wordmark">
                <span className="burmalda-wordmark-symbol">А</span> БУРМАЛДА <i>КОЛЕСО ФОРТУНЫ</i>
              </span>
              <button
                type="button"
                className="burmalda-close"
                onClick={close}
                aria-label="Закрыть Бурмалду"
              >
                <X size={21} />
              </button>
            </header>

            <div className={`burmalda-stage ${selected ? 'has-result' : ''}`}>
              <div className="burmalda-wheel-area">
                {stories.length > 0 && (
                  <div className="burmalda-fortune-wheel" key={round}>
                    <div className="burmalda-wheel-rim">
                      <div
                        className="burmalda-fortune-disk"
                        style={
                          {
                            '--landing-angle': `${8 * 360 - landingIndex * (360 / stories.length)}deg`,
                            '--wheel-background': wheelGradient(stories.length),
                          } as CSSProperties
                        }
                      >
                        {stories.map((item, index) => (
                          <span
                            className="burmalda-fortune-sector"
                            key={item.id}
                            style={
                              { '--angle': `${index * (360 / stories.length)}deg` } as CSSProperties
                            }
                          >
                            <span className="burmalda-sector-content">
                              <strong>{String(index + 1).padStart(2, '0')}</strong>
                              <small>{item.category}</small>
                            </span>
                          </span>
                        ))}
                        <span className="burmalda-fortune-hub">Б</span>
                      </div>
                    </div>
                    <span className="burmalda-fortune-pointer" />
                  </div>
                )}
                {stories.length === 0 && (
                  <div
                    className="burmalda-fortune-wheel burmalda-wheel-placeholder"
                    aria-hidden="true"
                  >
                    <div className="burmalda-wheel-rim">
                      <div
                        className="burmalda-fortune-disk"
                        style={{ '--wheel-background': wheelGradient(8) } as CSSProperties}
                      >
                        <span className="burmalda-fortune-hub">Б</span>
                      </div>
                    </div>
                    <span className="burmalda-fortune-pointer" />
                  </div>
                )}
                <div
                  className="burmalda-wheel-status"
                  role={spinning || loading ? 'status' : undefined}
                >
                  <span className="burmalda-wheel-status-kicker">
                    {loading
                      ? 'ЗАГРУЗКА НОВОСТЕЙ'
                      : spinning
                        ? 'КОЛЕСО ФОРТУНЫ'
                        : 'СЛУЧАЙНЫЙ ВЫБОР'}
                  </span>
                  <h2 id="burmalda-title">
                    {loading
                      ? 'Ищем новости'
                      : loadError
                        ? 'Новости недоступны'
                        : stories.length === 0
                          ? 'Пока нет новостей'
                          : spinning
                            ? 'Какая новость выпадет?'
                            : 'Новость найдена!'}
                  </h2>
                  <p id="burmalda-description">
                    {loading
                      ? 'Получаем материалы, уже сохранённые из подключённых источников.'
                      : loadError
                        ? 'Не удалось получить новости. Попробуйте ещё раз.'
                        : stories.length === 0
                          ? 'После первой загрузки источников здесь появятся свежие материалы.'
                          : spinning
                            ? `Колесо выбирает одну из ${stories.length} новостей. Ещё немного…`
                            : demo
                              ? 'Случайная тестовая история, не связанная с вашей компанией.'
                              : 'Случайная новость из подключённых источников, без персонального ранжирования.'}
                  </p>
                  {spinning && <span className="burmalda-spin-progress" aria-hidden="true" />}
                  {!demo && !loading && (loadError || stories.length === 0) && (
                    <button
                      className="burmalda-again"
                      type="button"
                      onClick={() => void loadStoredNews()}
                    >
                      <RotateCw size={18} /> Обновить новости
                    </button>
                  )}
                </div>
              </div>

              <div className="burmalda-outcome" aria-live="polite" aria-atomic="true">
                {selected && (
                  <article className="burmalda-news-reveal" key={selected.id}>
                    <span className="burmalda-reveal-badge">
                      СЛУЧАЙНАЯ НОВОСТЬ · №{' '}
                      {String(stories.findIndex((item) => item.id === selected.id) + 1).padStart(
                        2,
                        '0',
                      )}
                    </span>
                    <span className="burmalda-reveal-category">{selected.category}</span>
                    <h3>{selected.title}</h3>
                    <p>
                      {selected.summary ||
                        'В RSS-ленте есть только заголовок. Подробности — в оригинале публикации.'}
                    </p>
                    <span className="burmalda-reveal-note">
                      {demo
                        ? 'Вымышленная новость для демонстрации · без связи с контекстом и ML'
                        : `Источник: ${selected.sourceName}. ${newsOriginLabel(selected.origin ?? 'unknown')}. ${storyDate(selected)}Без персонального анализа.`}
                    </span>
                    {selected.url && (
                      <a
                        className="burmalda-source-link"
                        href={selected.url}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        Открыть оригинал ↗
                      </a>
                    )}
                    <button className="burmalda-again" type="button" onClick={() => startSpin()}>
                      <RotateCw size={18} /> Крутить ещё
                    </button>
                  </article>
                )}
              </div>
            </div>
            <footer className="burmalda-overlay-footer">
              {stories.length} {demo ? 'ИСТОРИЙ' : 'НОВОСТЕЙ'} <span>·</span> 1 СЛУЧАЙНЫЙ ВЫБОР
            </footer>
          </div>
        </dialog>
      )}
    </>
  );
}
