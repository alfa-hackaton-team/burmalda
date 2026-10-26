import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { App } from './App';
import { createDemoApi } from './demo';
import { ApiError } from './api';

describe('client scenario', () => {
  it.each([true, false])('opens Burmalda without a company context in demo=%s', async (demo) => {
    const api = createDemoApi('no-contexts');
    api.news = async () => [
      {
        article_id: '11111111-1111-4111-8111-111111111111',
        article_version_id: '22222222-2222-4222-8222-222222222222',
        title: 'Новость из источника',
        summary: 'Сохранённый текст',
        source_name: 'Издание',
        text_origin: 'full_article',
        url: null,
        published_at: null,
        fetched_at: '2026-10-25T10:00:00Z',
      },
    ];
    render(<App api={api} demo={demo} />);
    expect(
      within(screen.getByLabelText('Навигация')).queryByRole('button', { name: 'Бурмалда' }),
    ).not.toBeInTheDocument();
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'Бурмалда — открыть колесо случайных новостей' }));
    expect(screen.getByRole('dialog')).toBeVisible();
    expect(await screen.findByRole('heading', { name: 'Какая новость выпадет?' })).toBeVisible();
    expect(
      screen.queryByRole('combobox', { name: 'Компания и ракурс анализа' }),
    ).not.toBeInTheDocument();
  });

  it('opens an analysis, exposes evidence, saves feedback and restores the conversation between tabs', async () => {
    sessionStorage.clear();
    const user = userEvent.setup();
    render(<App api={createDemoApi()} demo />);
    await user.click(await screen.findByRole('button', { name: /^Требует внимания/ }));
    await screen.findByRole('heading', { name: 'Влияние на ваш бизнес' }, { timeout: 3000 });
    await user.click(screen.getByRole('button', { name: 'Разбор полезен' }));
    await screen.findByText('Оценка сохранена локально в деморежиме');
    await user.click(screen.getByRole('tab', { name: /^Основания/ }));
    expect(screen.getByText('Признаки бизнес-профиля')).toBeVisible();
    await user.click(screen.getByRole('tab', { name: 'Уточнить' }));
    await user.type(
      await screen.findByRole('textbox', { name: 'Уточняющий вопрос' }),
      'Что нужно проверить?',
    );
    await user.click(screen.getByRole('button', { name: /^Отправить$/ }));
    await screen.findByText('Учебный ответ', {}, { timeout: 3000 });
    await user.click(screen.getByRole('tab', { name: /^Разбор$/ }));
    expect(screen.getByRole('button', { name: 'Разбор полезен' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    await user.click(screen.getByRole('tab', { name: 'Уточнить' }));
    expect(screen.getByText('Что нужно проверить?')).toBeVisible();
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Компания и ракурс анализа' }),
      'demo-logistics-cfo',
    );
    await screen.findByRole('button', { name: /^Потенциальный риск/ });
    expect(
      screen.queryByRole('article', { name: 'Подробный разбор события' }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText('Что нужно проверить?')).not.toBeInTheDocument();
  }, 15_000);

  it('distinguishes an empty feed from an error', async () => {
    render(<App api={createDemoApi('empty')} demo />);
    expect(
      await screen.findByRole('heading', { name: 'Пока нет релевантных событий' }),
    ).toBeVisible();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('shows stored publications when the personal feed is not ready', async () => {
    const api = createDemoApi();
    api.feed = async () => {
      throw new ApiError({ code: 'PROFILE_NOT_READY', message: 'draft', retryable: false }, 409);
    };
    api.news = async () => [
      {
        article_id: '11111111-1111-4111-8111-111111111111',
        article_version_id: '22222222-2222-4222-8222-222222222222',
        title: 'Новости из PostgreSQL',
        summary: 'Извлечённый текст',
        source_name: 'Издание',
        text_origin: 'rss_summary',
        url: 'https://publisher.example/news',
        published_at: null,
        fetched_at: '2026-10-25T10:00:00Z',
      },
    ];
    render(<App api={api} demo={false} />);
    expect(await screen.findByRole('heading', { name: 'Новости из PostgreSQL' })).toBeVisible();
    expect(screen.getByRole('link', { name: 'Оригинал ↗' })).toHaveAttribute(
      'href',
      'https://publisher.example/news',
    );
  });

  it('discards a slow response after switching companies', async () => {
    const user = userEvent.setup();
    render(<App api={createDemoApi('slow')} demo />);
    await user.click(await screen.findByRole('button', { name: /^Требует внимания/ }));
    expect(within(screen.getByRole('article')).getByRole('status')).toBeVisible();
    await user.selectOptions(screen.getByRole('combobox'), 'demo-logistics-cfo');
    await waitFor(() => expect(screen.queryByRole('article')).not.toBeInTheDocument());
    await screen.findByRole('button', { name: /^Потенциальный риск/ });
    expect(screen.queryByText('Влияние на ваш бизнес')).not.toBeInTheDocument();
  });
});
