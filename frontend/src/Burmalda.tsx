import { useEffect, useRef, useState, type CSSProperties } from 'react';
import { RotateCw, X } from 'lucide-react';
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

export type BurmaldaNews = (typeof burmaldaNews)[number];
export const SPIN_DURATION_MS = 6400;

export function pickRandomNews(previousId?: string, random = Math.random): BurmaldaNews {
  const choices = burmaldaNews.filter((item) => item.id !== previousId);
  return choices[Math.min(choices.length - 1, Math.floor(random() * choices.length))];
}

function newsNumber(id: string): string {
  return String(burmaldaNews.findIndex((item) => item.id === id) + 1).padStart(2, '0');
}

export function Burmalda() {
  const dialog = useRef<HTMLDialogElement | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const previousId = useRef<string | undefined>(undefined);
  const pending = useRef<BurmaldaNews | undefined>(undefined);
  const [open, setOpen] = useState(false);
  const [spinning, setSpinning] = useState(false);
  const [selected, setSelected] = useState<BurmaldaNews>();
  const [landingIndex, setLandingIndex] = useState(0);
  const [round, setRound] = useState(0);

  useEffect(() => () => window.clearTimeout(timer.current), []);
  useEffect(() => {
    if (!open) return;
    const element = dialog.current;
    element?.showModal();
    return () => {
      if (element?.open) element.close();
    };
  }, [open]);

  function startSpin() {
    if (spinning) return;
    window.clearTimeout(timer.current);
    const news = pickRandomNews(previousId.current);
    pending.current = news;
    setLandingIndex(burmaldaNews.findIndex((item) => item.id === news.id));
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

  function close() {
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
        onClick={() => {
          setOpen(true);
          startSpin();
        }}
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
                <div className="burmalda-fortune-wheel" key={round}>
                  <div className="burmalda-wheel-rim">
                    <div
                      className="burmalda-fortune-disk"
                      style={
                        { '--landing-angle': `${8 * 360 - landingIndex * 45}deg` } as CSSProperties
                      }
                    >
                      {burmaldaNews.map((item, index) => (
                        <span
                          className="burmalda-fortune-sector"
                          key={item.id}
                          style={{ '--angle': `${index * 45}deg` } as CSSProperties}
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
                <div className="burmalda-wheel-status" role={spinning ? 'status' : undefined}>
                  <span className="burmalda-wheel-status-kicker">
                    {spinning ? 'КОЛЕСО ФОРТУНЫ' : 'СЛУЧАЙНЫЙ ВЫБОР'}
                  </span>
                  <h2 id="burmalda-title">
                    {spinning ? 'Какая новость выпадет?' : 'Новость найдена!'}
                  </h2>
                  <p id="burmalda-description">
                    {spinning
                      ? 'Колесо выбирает одну историю из восьми. Ещё немного…'
                      : 'Случайная тестовая история, не связанная с вашей компанией.'}
                  </p>
                  {spinning && <span className="burmalda-spin-progress" aria-hidden="true" />}
                </div>
              </div>

              <div className="burmalda-outcome" aria-live="polite" aria-atomic="true">
                {selected && (
                  <article className="burmalda-news-reveal" key={selected.id}>
                    <span className="burmalda-reveal-badge">
                      СЛУЧАЙНАЯ НОВОСТЬ · № {newsNumber(selected.id)}
                    </span>
                    <span className="burmalda-reveal-category">{selected.category}</span>
                    <h3>{selected.title}</h3>
                    <p>{selected.summary}</p>
                    <span className="burmalda-reveal-note">
                      Вымышленная новость для демонстрации · без связи с контекстом и ML
                    </span>
                    <button className="burmalda-again" type="button" onClick={startSpin}>
                      <RotateCw size={18} /> Крутить ещё
                    </button>
                  </article>
                )}
              </div>
            </div>
            <footer className="burmalda-overlay-footer">
              8 ИСТОРИЙ <span>·</span> 1 СЛУЧАЙНЫЙ ВЫБОР
            </footer>
          </div>
        </dialog>
      )}
    </>
  );
}
