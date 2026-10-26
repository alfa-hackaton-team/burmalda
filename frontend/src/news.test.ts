import { describe, expect, it } from 'vitest';
import { newsSummary } from './news';
import type { NewsItem } from './types';

describe('stored news presentation', () => {
  it('does not show an RSS headline twice as its summary', () => {
    const item: NewsItem = {
      article_id: 'article',
      article_version_id: 'version',
      title: 'Federal Reserve publishes minutes',
      summary: 'Federal Reserve publishes minutes',
      source_name: 'Federal Reserve',
      text_origin: 'rss_summary',
      url: 'https://publisher.example/story',
      published_at: null,
      fetched_at: '2026-10-25T10:00:00Z',
    };
    expect(newsSummary(item)).toBe('');
    expect(
      newsSummary({
        ...item,
        summary:
          'Federal Reserve publishes minutes with details on rates and policy decisions for banks',
      }),
    ).toBe('with details on rates and policy decisions for banks');
  });
});
