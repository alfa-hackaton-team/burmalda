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
    render(<Burmalda />);

    fireEvent.click(
      screen.getByRole('button', { name: 'Бурмалда — открыть колесо случайных новостей' }),
    );
    expect(screen.getByRole('dialog')).toBeVisible();
    expect(screen.getByRole('status')).toHaveTextContent('Колесо выбирает одну историю');
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
});
