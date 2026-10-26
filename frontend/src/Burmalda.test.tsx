import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Burmalda, SPIN_DURATION_MS } from './Burmalda';

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('Burmalda', () => {
  it('reveals a random test story and picks a different one on the next spin', () => {
    vi.useFakeTimers();
    vi.spyOn(Math, 'random').mockReturnValue(0);
    render(<Burmalda demo />);

    fireEvent.click(
      screen.getByRole('button', { name: 'Бурмалда — открыть колесо случайных новостей' }),
    );
    expect(screen.getByRole('dialog')).toBeVisible();
    expect(screen.getByRole('status')).toHaveTextContent('Колесо выбирает одну из 8 новостей');
    act(() => vi.advanceTimersByTime(SPIN_DURATION_MS - 1));
    expect(
      screen.queryByRole('heading', { name: 'Ночная смена для маленьких идей' }),
    ).not.toBeInTheDocument();
    act(() => vi.advanceTimersByTime(1));
    expect(screen.getByRole('heading', { name: 'Ночная смена для маленьких идей' })).toBeVisible();
    expect(screen.getByText(/СЛУЧАЙНАЯ НОВОСТЬ · № 01/)).toBeVisible();

    fireEvent.click(screen.getByRole('button', { name: 'Крутить ещё' }));
    act(() => vi.advanceTimersByTime(SPIN_DURATION_MS));
    expect(
      screen.getByRole('heading', { name: 'Склад научился считать свободные полки' }),
    ).toBeVisible();
    expect(screen.getByText(/СЛУЧАЙНАЯ НОВОСТЬ · № 02/)).toBeVisible();
    expect(
      screen.queryByRole('heading', { name: 'Ночная смена для маленьких идей' }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText('Вымышленная новость для демонстрации · без связи с контекстом и ML'),
    ).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: 'Закрыть Бурмалду' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('loads stored news once per cache window and links to the original', async () => {
    vi.useFakeTimers();
    vi.spyOn(Math, 'random').mockReturnValue(0);
    const loadNews = vi.fn(async () => [
      {
        article_id: '11111111-1111-4111-8111-111111111111',
        article_version_id: '22222222-2222-4222-8222-222222222222',
        title: 'Сохранённая новость',
        summary: 'Текст из базы данных.',
        source_name: 'Издание',
        text_origin: 'full_article' as const,
        url: 'https://publisher.example/story',
        published_at: null,
        fetched_at: '2026-10-25T10:00:00Z',
      },
    ]);
    render(<Burmalda demo={false} loadNews={loadNews} />);
    await act(async () => {
      fireEvent.click(
        screen.getByRole('button', { name: 'Бурмалда — открыть колесо случайных новостей' }),
      );
    });
    expect(loadNews).toHaveBeenCalledTimes(1);
    act(() => vi.advanceTimersByTime(SPIN_DURATION_MS));
    expect(screen.getByRole('heading', { name: 'Сохранённая новость' })).toBeVisible();
    expect(screen.getByRole('link', { name: 'Открыть оригинал ↗' })).toHaveAttribute(
      'href',
      'https://publisher.example/story',
    );
    fireEvent.click(screen.getByRole('button', { name: 'Закрыть Бурмалду' }));
    fireEvent.click(
      screen.getByRole('button', { name: 'Бурмалда — открыть колесо случайных новостей' }),
    );
    expect(loadNews).toHaveBeenCalledTimes(1);
  });

  it('shows an empty state without inventing a news item', async () => {
    render(<Burmalda demo={false} loadNews={async () => []} />);
    await act(async () => {
      fireEvent.click(
        screen.getByRole('button', { name: 'Бурмалда — открыть колесо случайных новостей' }),
      );
    });
    expect(screen.getByRole('heading', { name: 'Пока нет новостей' })).toBeVisible();
    expect(screen.queryByText('Ночная смена для маленьких идей')).not.toBeInTheDocument();
  });

  it('draws the same number of wheel sectors as loaded news', async () => {
    const loadNews = async () =>
      [1, 2, 3].map((number) => ({
        article_id: `00000000-0000-4000-8000-00000000000${number}`,
        article_version_id: `10000000-0000-4000-8000-00000000000${number}`,
        title: `Новость ${number}`,
        summary: `Содержание ${number}`,
        source_name: 'Источник',
        text_origin: 'full_article' as const,
        url: null,
        published_at: null,
        fetched_at: '2026-10-25T10:00:00Z',
      }));
    const { container } = render(<Burmalda demo={false} loadNews={loadNews} />);
    await act(async () => {
      fireEvent.click(
        screen.getByRole('button', { name: 'Бурмалда — открыть колесо случайных новостей' }),
      );
    });
    expect(container.querySelectorAll('.burmalda-fortune-sector')).toHaveLength(3);
    expect(
      (container.querySelector('.burmalda-fortune-disk') as HTMLElement).style.getPropertyValue(
        '--wheel-background',
      ),
    ).toContain('from -60deg');
  });
});
