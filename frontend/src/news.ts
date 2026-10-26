import type { NewsItem } from './types';

export function newsOriginLabel(origin: NewsItem['text_origin']): string {
  switch (origin) {
    case 'full_article':
      return 'текст статьи';
    case 'rss_summary':
      return 'RSS-анонс';
    case 'pdf_extracted':
      return 'текст PDF';
    default:
      return 'сохранённый текст';
  }
}

export function newsSummary(item: NewsItem): string {
  const summary = item.summary.replace(/\s+/g, ' ').trim();
  if (item.text_origin !== 'rss_summary') return summary;
  const title = item.title.replace(/\s+/g, ' ').trim();
  if (summary.toLocaleLowerCase().startsWith(title.toLocaleLowerCase())) {
    const remainder = summary.slice(title.length).trim();
    return remainder.length >= 30 ? remainder : '';
  }
  return summary;
}
